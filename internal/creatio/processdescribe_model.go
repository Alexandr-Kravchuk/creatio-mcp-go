package creatio

// clio re-serializes the server's describe graph through its typed model (IProcessDescriber.cs): every type
// writes its declared members in declaration order, leaves a null one out, and - where the type carries an
// overflow bag ([JsonExtensionData]) - writes the server's other members after them, verbatim; a type without
// one drops them. The reply's text, its size and the 8.1.0.142 checksum all depend on that order, so the graph
// is projected through the same declarations here (taken from clio 8.1.0.142).

import "strings"

type describeMember struct {
	name string
	typ  string // a describeTypes key, or "" for a value written as it is
	list bool
}

type describeType struct {
	members []describeMember
	ext     bool
}

func describeMembers(spec string) []describeMember {
	members := []describeMember{}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		member := describeMember{name: item}
		if name, typ, ok := strings.Cut(item, ":"); ok {
			member.name, member.typ = name, typ
			if strings.HasPrefix(typ, "[]") {
				member.typ, member.list = typ[2:], true
			}
		}
		members = append(members, member)
	}
	return members
}

var describeTypes = map[string]describeType{
	"Element": {describeMembers("name, uid, caption, type, buildType, userTaskName, managerItemUId, position, size, useBackgroundMode, " +
		"parameters:[]Parameter, signal:Signal, filter:Filter, filterDecodedCompletely, email:Email, openEditPage:OpenEditPage, " +
		"preconfiguredPage:PreconfiguredPage, subProcess:SubProcess, connections:[]Connection, deprecated, performer:Performer, " +
		"writesConnectionsAtRuntime, approval:Approval, formula:Formula, scriptTask:ScriptTask, fileProcessing:FileProcessing"), true},
	"Approval": {describeMembers("purpose, object, objectUId, recordId, recordIdDisplay, approverType, approverEmployee, " +
		"approverEmployeeDisplay, approverRole, approverRoleDisplay, allowDelegation, notifyApprover, approverEmailTemplate, " +
		"approverEmailTemplateDisplay, notifyAuthor, authorEmailTemplate, authorEmailTemplateDisplay, recipient, ignoreEmailErrors, approvalSchemaUId"), true},
	"OpenEditPage": {describeMembers("page, pageSchemaUId, object, pageTypeUId, editMode, defaultValues, recordId, recommendation, hint, " +
		"resultsByColumn:ResultsByColumn, logActivity:LogActivity, performer:Performer, completionMode"), true},
	"ResultsByColumn":   {describeMembers("enabled, column, columnUId"), true},
	"LogActivity":       {describeMembers("enabled, startIn:Interval, duration:Interval, remindIn:Interval, showInCalendar, priority, priorityId"), true},
	"Interval":          {describeMembers("value, unit, period"), true},
	"Email":             {describeMembers("mode, sender, senderDisplay, subject, messageSource, template, templateDisplay, templateObject, templateEntity:Parameter, hasBody, body, importance, ignoreErrors, to:[]Parameter, cc:[]Parameter, bcc:[]Parameter, performer:Performer"), true},
	"SubProcess":        {describeMembers("process, processUId, processCaption, multiInstance, inSync, multiInstanceOptions:MultiInstance"), true},
	"MultiInstance":     {describeMembers("enabled, executionMode, ignoreErrors, inputCollection, outputCollection, completedIterationsCount, terminatedIterationsCount, totalIterationsCount, calleeInSync"), true},
	"PreconfiguredPage": {describeMembers("page, pageUiType, performer:PagePerformer, buttons:[]PageButton, recommendation, dataSources:[]PageDataSource, connectedObject, connectedObjectRecord, inSync, shadowedPageParameters"), true},
	"PagePerformer":     {describeMembers("type, contact, role, showPage"), true},
	"PageDataSource":    {describeMembers("name, entitySchemaName, parameter"), true},
	"PageButton":        {describeMembers("name, caption, event, validate"), true},
	"Performer":         {describeMembers("type, contact, role, roleDisplay, showPage"), true},
	"Connection":        {describeMembers("column, registered, source, value, recordId, referenceSchema, processParameter, sourceElement, sourceElementParameter, expression"), false},
	"Signal":            {describeMembers("entity, entitySchemaUId, on, changedColumns"), false},
	// DescribedFilter : DescribedFilterGroup - System.Text.Json writes the derived type's member first.
	"Filter":           {describeMembers("object, logicalOperation, conditions:[]FilterCondition, groups:[]FilterGroup"), false},
	"FilterGroup":      {describeMembers("logicalOperation, conditions:[]FilterCondition, groups:[]FilterGroup"), false},
	"FilterCondition":  {describeMembers("column, comparison, value, displayValue, processParameter, elementParameter:FilterElementRef, expression, macro, macroArgument, datePart"), false},
	"FilterElementRef": {describeMembers("elementName, parameter"), false},
	"Flow":             {describeMembers("source, target, kind, condition, results, resultsActivity, branchesOnActivityResult, label, geometry:Geometry"), true},
	"Geometry":         {describeMembers("start, points, end, exitSide, entrySide"), true},
	"Parameter": {describeMembers("name, caption, description, uid, type, direction, isResult, isOutput, isRequired, referenceSchema, source, " +
		"value, valueOmitted, valueDisplay, sourceElement, sourceElementParameter, sourceColumn, tag, itemProperties:[]Parameter"), false},
	"Formula":         {describeMembers("body, target:FormulaTarget"), true},
	"ScriptTask":      {describeMembers("body, forInterpretedProcess"), true},
	"Using":           {describeMembers("namespace, alias, ignored"), true},
	"FormulaTarget":   {describeMembers("resultProcessParameter, elementName, elementParameter, entityColumnUId, unresolved"), true},
	"FileProcessing":  {describeMembers("source, action, actionStored, attachments:FileAttachments, saveTo:FileSaveTo, outputs:[]FileOutput, issues"), true},
	"FileAttachments": {describeMembers("object, storage, fileObject, linkColumn, recordId:FileValueSource, numberOfRecords, sort:FileSort"), true},
	"FileSort":        {describeMembers("column, direction"), true},
	"FileSaveTo":      {describeMembers("object, recordId:FileValueSource, linkColumn, storage, fileObject"), true},
	"FileValueSource": {describeMembers("value, processParameter, sourceElement, sourceElementParameter, sourceColumn, expression, display"), true},
	"FileOutput":      {describeMembers("parameter, item"), true},
	"Tracing":         {describeMembers("enabled, turnOffDate"), true},
}

// processDescribeRootTypes are the typed members of DescribeProcessResult itself.
var processDescribeRootTypes = map[string]describeMember{
	"elements": {name: "elements", typ: "Element", list: true}, "flows": {name: "flows", typ: "Flow", list: true},
	"parameters": {name: "parameters", typ: "Parameter", list: true}, "usings": {name: "usings", typ: "Using", list: true},
	"tracing": {name: "tracing", typ: "Tracing"},
}

// processDescribeProject writes value as clio's model type typ does.
func processDescribeProject(value *jnode, typ string, list bool) *jnode {
	if value == nil || value.kind == jkNull {
		return nil
	}
	if list {
		if !value.isArray() {
			return value
		}
		projected := newArray()
		for _, item := range value.items {
			if item == nil || item.kind == jkNull {
				projected.items = append(projected.items, jsonNullNode())
				continue
			}
			projected.items = append(projected.items, processDescribeProject(item, typ, false))
		}
		return projected
	}
	spec, known := describeTypes[typ]
	if typ == "" || !known || !value.isObject() {
		return value
	}
	projected := newObject()
	used := map[string]bool{}
	for _, member := range spec.members {
		key := ""
		if _, ok := value.props[member.name]; ok {
			key = member.name
		} else {
			for _, candidate := range value.keys {
				if strings.EqualFold(candidate, member.name) && !used[candidate] {
					key = candidate
					break
				}
			}
		}
		if key == "" {
			continue
		}
		used[key] = true
		if written := processDescribeProject(value.props[key], member.typ, member.list); written != nil {
			projected.set(member.name, written)
		}
	}
	if spec.ext {
		for _, key := range value.keys {
			if !used[key] {
				projected.set(key, value.props[key])
			}
		}
	}
	return projected
}
