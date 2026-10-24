package pagesmoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"
	"sync/atomic"
)

// One Chrome DevTools Protocol session over the browser's DevTools pipe (clio's CdpSession over
// PipeCdpTransport): every message is UTF-8 JSON followed by one NUL byte, in both directions. One receive loop
// reads every message: an answer completes the pending command with the same id, an event goes to the
// handlers. Sends are serialized. When the channel closes or fails, every pending command fails at once.
//
// The pipe reaches the browser target; once routeToSession names the flat session of an attached page, every
// command carries that session id, only that page's events are raised, and the page being detached closes the
// session.

const (
	// maxMessageBytes bounds a reply; the rest of a longer one is discarded and its command fails.
	maxMessageBytes    = 4 * 1024 * 1024
	replyTooLargeKey   = "clio.replyTooLarge"
	sentinelPrefix     = 64
	readBufferBytes    = 64 * 1024
	detachedFromTarget = "Target.detachedFromTarget"
)

var leadingID = regexp.MustCompile(`\A\s*\{\s*"id"\s*:\s*(\d{1,9})\s*[,}]`)

// cdpCommandError is CdpCommandException: the browser answered a command with an error.
type cdpCommandError struct {
	method  string
	message string
}

func (e *cdpCommandError) Error() string {
	return "CDP command '" + e.method + "' failed: " + e.message
}

// cdpClosedError is CdpSessionClosedException: the DevTools connection is gone.
type cdpClosedError struct{ message string }

func (e *cdpClosedError) Error() string { return e.message }

// cdpTooLargeError is CdpReplyTooLargeException.
type cdpTooLargeError struct{ method string }

func (e *cdpTooLargeError) Error() string {
	return fmt.Sprintf("CDP command '%s' failed: its reply exceeded %d MB and was discarded.", e.method, maxMessageBytes/(1024*1024))
}

// cdpEvent is one CDP event: its method and params (an empty object when absent).
type cdpEvent struct {
	Method string
	Params json.RawMessage
}

type cdpReply struct {
	result json.RawMessage
	err    error
}

type pendingCommand struct {
	method string
	reply  chan cdpReply
}

type cdpSession struct {
	from io.ReadCloser
	to   io.WriteCloser

	sendMutex sync.Mutex
	lastID    atomic.Int64

	stateMutex sync.Mutex
	pending    map[int64]*pendingCommand
	closed     string // the reason the session closed; "" while open
	sessionID  string
	handlers   []func(cdpEvent)

	loopDone    chan struct{}
	disposeOnce sync.Once
}

func newCdpSession(from io.ReadCloser, to io.WriteCloser) *cdpSession {
	session := &cdpSession{from: from, to: to, pending: map[int64]*pendingCommand{}, loopDone: make(chan struct{})}
	go session.receiveLoop()
	return session
}

// onEvent adds a handler; it runs on the receive loop and must be quick. A panic in it is swallowed.
func (s *cdpSession) onEvent(handler func(cdpEvent)) {
	s.stateMutex.Lock()
	defer s.stateMutex.Unlock()
	s.handlers = append(s.handlers, handler)
}

// routeToSession routes every later command to an attached page's flat session.
func (s *cdpSession) routeToSession(sessionID string) {
	s.stateMutex.Lock()
	defer s.stateMutex.Unlock()
	s.sessionID = sessionID
}

func (s *cdpSession) closedError(method string) error {
	s.stateMutex.Lock()
	reason := s.closed
	s.stateMutex.Unlock()
	if reason == "" {
		reason = "the session closed"
	}
	return &cdpClosedError{message: "CDP command '" + method + "' could not complete: " + reason + "."}
}

func (s *cdpSession) isClosed() bool {
	s.stateMutex.Lock()
	defer s.stateMutex.Unlock()
	return s.closed != ""
}

// send sends one command and waits for its answer; params nil sends {}. ctx ends the wait only: the browser may
// still run the command.
func (s *cdpSession) send(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if s.isClosed() {
		return nil, s.closedError(method)
	}
	id := s.lastID.Add(1)
	pending := &pendingCommand{method: method, reply: make(chan cdpReply, 1)}
	s.stateMutex.Lock()
	s.pending[id] = pending
	sessionID := s.sessionID
	s.stateMutex.Unlock()
	defer func() {
		s.stateMutex.Lock()
		delete(s.pending, id)
		s.stateMutex.Unlock()
	}()
	if params == nil {
		params = struct{}{}
	}
	message := struct {
		ID        int64  `json:"id"`
		Method    string `json:"method"`
		Params    any    `json:"params"`
		SessionID string `json:"sessionId,omitempty"`
	}{id, method, params, sessionID}
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	s.sendMutex.Lock()
	if s.isClosed() {
		s.sendMutex.Unlock()
		return nil, s.closedError(method)
	}
	_, err = s.to.Write(append(encoded, 0))
	s.sendMutex.Unlock()
	if err != nil {
		return nil, &cdpClosedError{message: fmt.Sprintf("CDP command '%s' could not be sent: the DevTools connection failed (%s).", method, err.Error())}
	}
	if s.isClosed() {
		s.failPending()
	}
	select {
	case reply := <-pending.reply:
		return reply.result, reply.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *cdpSession) receiveLoop() {
	defer close(s.loopDone)
	reason := "the browser closed the DevTools connection"
	reader := &pipeMessageReader{from: s.from}
	for {
		message, err := reader.next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				reason = "the DevTools connection failed: " + err.Error()
			}
			break
		}
		s.dispatch(message)
	}
	s.close(reason)
}

func (s *cdpSession) close(reason string) {
	s.stateMutex.Lock()
	if s.closed == "" {
		s.closed = reason
	}
	s.stateMutex.Unlock()
	s.failPending()
}

func (s *cdpSession) failPending() {
	s.stateMutex.Lock()
	pending := make([]*pendingCommand, 0, len(s.pending))
	for _, command := range s.pending {
		pending = append(pending, command)
	}
	s.stateMutex.Unlock()
	for _, command := range pending {
		select {
		case command.reply <- cdpReply{err: s.closedError(command.method)}:
		default:
		}
	}
}

// dispatch handles one message. A message that is not JSON, or has neither an id nor a method, is ignored.
func (s *cdpSession) dispatch(message string) {
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(message), &root) != nil || root == nil {
		return
	}
	if rawID, ok := root["id"]; ok {
		var id int64
		if json.Unmarshal(rawID, &id) == nil && id >= -1<<31 && id < 1<<31 {
			if _, tooLarge := root[replyTooLargeKey]; tooLarge {
				s.complete(id, func(method string) cdpReply { return cdpReply{err: &cdpTooLargeError{method: method}} })
				return
			}
			s.complete(id, func(method string) cdpReply { return replyOf(method, root) })
			return
		}
	}
	rawMethod, ok := root["method"]
	if !ok {
		return
	}
	var method string
	if json.Unmarshal(rawMethod, &method) != nil {
		return
	}
	params := root["params"]
	if params == nil {
		params = json.RawMessage("{}")
	}
	eventSession, hasSession := stringMember(root, "sessionId")
	s.stateMutex.Lock()
	current := s.sessionID
	handlers := append([]func(cdpEvent){}, s.handlers...)
	s.stateMutex.Unlock()
	if hasSession == (current != "") && eventSession == current {
		for _, handler := range handlers {
			raise(handler, cdpEvent{Method: method, Params: params})
		}
		return
	}
	if !hasSession && method == detachedFromTarget && current != "" {
		var detached map[string]json.RawMessage
		if json.Unmarshal(params, &detached) == nil {
			if session, ok := stringMember(detached, "sessionId"); ok && session == current {
				s.close("the browser detached the page")
			}
		}
	}
}

func raise(handler func(cdpEvent), event cdpEvent) {
	defer func() { _ = recover() }()
	handler(event)
}

func (s *cdpSession) complete(id int64, reply func(method string) cdpReply) {
	s.stateMutex.Lock()
	pending, ok := s.pending[id]
	s.stateMutex.Unlock()
	if !ok {
		return
	}
	select {
	case pending.reply <- reply(pending.method):
	default:
	}
}

func replyOf(method string, root map[string]json.RawMessage) cdpReply {
	if rawError, ok := root["error"]; ok {
		text := string(rawError)
		var object map[string]json.RawMessage
		if json.Unmarshal(rawError, &object) == nil && object != nil {
			if message, ok := stringMember(object, "message"); ok {
				text = message
			}
		}
		return cdpReply{err: &cdpCommandError{method: method, message: text}}
	}
	if result, ok := root["result"]; ok {
		return cdpReply{result: result}
	}
	return cdpReply{result: json.RawMessage("{}")}
}

// stringMember reads a string member; ok false when it is absent or not a string.
func stringMember(object map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := object[name]
	if !ok {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

// dispose closes the pipe (Chromium's own signal to shut down) and fails every pending command.
func (s *cdpSession) dispose() {
	s.disposeOnce.Do(func() {
		s.stateMutex.Lock()
		if s.closed == "" {
			s.closed = "the session was disposed"
		}
		s.stateMutex.Unlock()
		_ = s.to.Close()
		_ = s.from.Close()
		s.failPending()
	})
}

// pipeMessageReader splits the reply pipe into NUL-terminated messages (PipeCdpTransport.ReceiveAsync). A
// message over maxMessageBytes keeps only its head, where a reply's id is, and becomes a sentinel that fails
// that reply's command.
type pipeMessageReader struct {
	from     io.Reader
	buffer   []byte
	start    int
	end      int
	message  bytes.Buffer
	overflow bool
}

func (r *pipeMessageReader) next() (string, error) {
	if r.buffer == nil {
		r.buffer = make([]byte, readBufferBytes)
	}
	for {
		if r.start == r.end {
			read, err := r.from.Read(r.buffer)
			if read == 0 {
				if err == nil {
					continue
				}
				// A message cut off by the browser's exit is never answered: drop it.
				return "", err
			}
			r.start, r.end = 0, read
		}
		chunk := r.buffer[r.start:r.end]
		terminator := bytes.IndexByte(chunk, 0)
		if terminator < 0 {
			r.append(chunk)
			r.start = r.end
			continue
		}
		r.append(chunk[:terminator])
		r.start += terminator + 1
		return r.take(), nil
	}
}

func (r *pipeMessageReader) append(chunk []byte) {
	if r.overflow {
		return
	}
	if r.message.Len()+len(chunk) <= maxMessageBytes {
		r.message.Write(chunk)
		return
	}
	r.overflow = true
	if missing := sentinelPrefix - r.message.Len(); missing > 0 {
		r.message.Write(chunk[:min(missing, len(chunk))])
	}
	if r.message.Len() > sentinelPrefix {
		r.message.Truncate(sentinelPrefix)
	}
}

func (r *pipeMessageReader) take() string {
	var message string
	if r.overflow {
		message = oversizedSentinel(r.message.Bytes())
	} else {
		message = r.message.String()
	}
	r.message.Reset()
	r.overflow = false
	return message
}

// oversizedSentinel replaces a message over the cap: the reply's id and the marker, or {} (ignored) for an
// event nobody waits for.
func oversizedSentinel(prefix []byte) string {
	head := prefix[:min(len(prefix), sentinelPrefix)]
	match := leadingID.FindSubmatch(head)
	if match == nil {
		return "{}"
	}
	return `{"id":` + string(match[1]) + `,"` + replyTooLargeKey + `":true}`
}
