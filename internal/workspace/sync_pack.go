package workspace

// Packing for push-workspace: clio's WorkingDirectoriesProvider temporary folders, PackageUtilities'
// element copy, PackageArchiver.Pack (skipPdb, .clio/clioignore, the descriptor check of
// PackageItemDescriptorCheck) and CompressionUtilities' .gz format, and ZipFile.CreateFromDirectory for the
// CreatioPackages.zip that goes to the installer. Files are taken in the order .NET enumerates them: a
// directory's entries as the file system lists them, files first, subdirectories queued and walked after it.

import (
	"archive/zip"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"
)

// tempBase is WorkingDirectoriesProvider.BaseTempDirectory: CLIO_WORKING_DIRECTORY or the system temporary
// folder, then "clio".
func tempBase() string {
	if base := os.Getenv("CLIO_WORKING_DIRECTORY"); base != "" {
		return filepath.Join(base, "clio")
	}
	return filepath.Join(os.TempDir(), "clio")
}

// newTempDirectory is WorkingDirectoriesProvider.CreateTempDirectory(): <base>/<GUID in "N" form>.
func newTempDirectory() (string, error) {
	path := filepath.Join(tempBase(), strings.ReplaceAll(newGUID(), "-", ""))
	if err := os.MkdirAll(path, 0o777); err != nil {
		return "", err
	}
	return path, nil
}

// withTempDirectory is CreateTempDirectory(Action<string>): the folder's creation and deletion are logged.
func withTempDirectory(log *Log, action func(string) error) error {
	log.Info("Creating temporary directory...")
	path, err := newTempDirectory()
	if err != nil {
		return err
	}
	log.Info("Created temporary directory " + path)
	defer deleteTempDirectory(log, path)
	return action(path)
}

// withQuietTempDirectory is CreateTempDirectory<T>(Func<string, T>): only the deletion is logged.
func withQuietTempDirectory(log *Log, action func(string) error) error {
	path, err := newTempDirectory()
	if err != nil {
		return err
	}
	defer deleteTempDirectory(log, path)
	return action(path)
}

func deleteTempDirectory(log *Log, path string) {
	if directoryExists(path) {
		if os.RemoveAll(path) == nil {
			log.Info("Deleted temporary directory " + path)
		}
	}
}

// listDirectory lists a folder in the file system's own order (readdir, FindFirstFile), as .NET's
// enumeration does; os.ReadDir would sort it.
func listDirectory(path string) ([]os.DirEntry, error) {
	folder, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer folder.Close()
	return folder.ReadDir(-1)
}

func entryIsDirectory(parent string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&fs.ModeSymlink != 0 {
		return directoryExists(filepath.Join(parent, entry.Name()))
	}
	return false
}

// directoryFiles is Directory.GetFiles(path) / GetDirectories(path): one level, in listing order.
func directoryFiles(path string) (files, folders []string, err error) {
	entries, err := listDirectory(path)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		full := filepath.Join(path, entry.Name())
		if entryIsDirectory(path, entry) {
			folders = append(folders, full)
		} else {
			files = append(files, full)
		}
	}
	return files, folders, nil
}

// allFiles is Directory.GetFiles(root, "*.*", SearchOption.AllDirectories): breadth first, in listing order.
// maxDepth limits the folders walked below root (-1: no limit); skipHidden leaves out names that start with a
// dot, as EnumerationOptions' default AttributesToSkip does on Unix.
func allFiles(root string, maxDepth int, skipHidden bool) ([]string, error) {
	type pending struct {
		path  string
		depth int
	}
	queue := []pending{{root, 0}}
	var files []string
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		entries, err := listDirectory(current.path)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if skipHidden && strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			full := filepath.Join(current.path, entry.Name())
			if entryIsDirectory(current.path, entry) {
				if maxDepth < 0 || current.depth < maxDepth {
					queue = append(queue, pending{full, current.depth + 1})
				}
				continue
			}
			files = append(files, full)
		}
	}
	return files, nil
}

// copyDirectory is FileSystem.CopyDirectory: the destination created (or emptied), files copied over, then
// every subfolder the same way.
func copyDirectory(source, destination string) error {
	if err := createOrClearDirectory(destination); err != nil {
		return err
	}
	files, folders, err := directoryFiles(source)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := copyFile(file, filepath.Join(destination, filepath.Base(file))); err != nil {
			return err
		}
	}
	for _, folder := range folders {
		if err := copyDirectory(folder, filepath.Join(destination, filepath.Base(folder))); err != nil {
			return err
		}
	}
	return nil
}

// copyFile is File.Copy with overwrite: the content, and on Unix the source's mode.
func copyFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("Could not find file '%s'.", source)
		}
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(destination, info.Mode().Perm())
}

// createOrClearDirectory is CreateOrOverwriteExistsDirectoryIfNeeded(path, true) / CreateOrClearDirectory.
func createOrClearDirectory(path string) error {
	if directoryExists(path) {
		return clearDirectory(path)
	}
	return os.MkdirAll(path, 0o777)
}

// packageElements are PackageUtilities.PackageElementNames.
var packageElements = []string{"Assemblies", "Bin", "Data", "Files", "Resources", "Schemas", "SqlScripts"}

// packageContentFolder is PackageUtilities.GetPackageContentFolderPath: a repository package keeps its content
// in its only branches/<version> folder.
func packageContentFolder(packagePath string) (string, error) {
	branches := combinePath(packagePath, "branches")
	if !directoryExists(branches) {
		return packagePath, nil
	}
	_, folders, err := directoryFiles(branches)
	if err != nil {
		return "", err
	}
	if len(folders) == 1 {
		return folders[0], nil
	}
	return "", fmt.Errorf("Unsupported package folder structure.Expected structure contains one package version in folder '%s'.", branches)
}

// folderWithoutDescriptor is PackageItemFolderWithoutDescriptor.
type folderWithoutDescriptor struct {
	path  string
	files []string
}

// descriptorMissingError is PackageItemDescriptorMissingException.
type descriptorMissingError struct{ folders []folderWithoutDescriptor }

func (e *descriptorMissingError) Error() string {
	subject := "This package folder has no descriptor.json"
	if len(e.folders) != 1 {
		subject = fmt.Sprintf("These %d package folders have no descriptor.json", len(e.folders))
	}
	var described []string
	for index, folder := range e.folders {
		if index == 20 {
			break
		}
		described = append(described, folder.path+" ("+describeFolderFiles(folder.files)+"; "+folderAdvice(folder.files)+")")
	}
	more := ""
	if len(e.folders) > 20 {
		more = fmt.Sprintf("; and %d more", len(e.folders)-20)
	}
	return subject + ": " + strings.Join(described, "; ") + more + ". Creatio rejects the whole installation when a folder " +
		"under Schemas/ or Data/ has no descriptor.json (\"Invalid descriptor\"), so clio stopped before building the " +
		"package archive. A folder that holds only Localization files is what git leaves behind when a data binding is " +
		"deleted: delete it. A folder that still holds other files is an element that lost its descriptor.json: " +
		"restore the descriptor, for example from git, because deleting the folder can remove the element from " +
		"the environment on the next install."
}

func describeFolderFiles(files []string) string {
	listed := strings.Join(files[:min(3, len(files))], ", ")
	switch {
	case len(files) == 1:
		return "1 file: " + listed
	case len(files) <= 3:
		return fmt.Sprintf("%d files: %s", len(files), listed)
	}
	return fmt.Sprintf("%d files, e.g. %s", len(files), listed)
}

func operatingSystemFile(file string) bool {
	name := file[strings.LastIndex(file, "/")+1:]
	for _, known := range []string{".DS_Store", "Thumbs.db", "desktop.ini"} {
		if strings.EqualFold(name, known) {
			return true
		}
	}
	return false
}

func folderAdvice(files []string) string {
	onlySystem := true
	var element []string
	for _, file := range files {
		if !operatingSystemFile(file) {
			onlySystem = false
			element = append(element, file)
		}
	}
	if onlySystem {
		return "only operating system files, delete the folder"
	}
	onlyLocalization := len(element) > 0
	for _, file := range element {
		if len(file) < len("Localization/") || !strings.EqualFold(file[:len("Localization/")], "Localization/") {
			onlyLocalization = false
		}
	}
	if onlyLocalization {
		return "only Localization files, delete the folder"
	}
	return "element files, restore descriptor.json"
}

// foldersWithoutDescriptor is PackageItemDescriptorCheck.FindFoldersWithoutDescriptor over package-relative
// paths: folders directly under Schemas/ or Data/ that put files into the archive without descriptor.json.
func foldersWithoutDescriptor(relativePaths []string) []folderWithoutDescriptor {
	filesByFolder := map[string]map[string]bool{}
	withDescriptor := map[string]bool{}
	for _, path := range relativePaths {
		var segments []string
		for _, segment := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
			segments = append(segments, segment)
		}
		if len(segments) < 3 || (segments[0] != "Schemas" && segments[0] != "Data") {
			continue
		}
		folder := segments[0] + "/" + segments[1]
		if filesByFolder[folder] == nil {
			filesByFolder[folder] = map[string]bool{}
		}
		filesByFolder[folder][strings.Join(segments[2:], "/")] = true
		if len(segments) == 3 && strings.EqualFold(segments[2], "descriptor.json") {
			withDescriptor[folder] = true
		}
	}
	var names []string
	for folder := range filesByFolder {
		if !withDescriptor[folder] {
			names = append(names, folder)
		}
	}
	sort.Strings(names)
	var found []folderWithoutDescriptor
	for _, folder := range names {
		var files []string
		for file := range filesByFolder[folder] {
			files = append(files, file)
		}
		sort.Strings(files)
		found = append(found, folderWithoutDescriptor{path: folder, files: files})
	}
	return found
}

// packPackage is PackageArchiver.Pack(packagePath, packedPath, skipPdb: true, overwrite: true).
func packPackage(log *Log, packagePath, packedPath string) error {
	return withTempDirectory(log, func(temp string) error {
		content, err := packageContentFolder(packagePath)
		if err != nil {
			return err
		}
		if err := createOrClearDirectory(temp); err != nil {
			return err
		}
		for _, element := range packageElements {
			source := combinePath(content, element)
			if directoryExists(source) {
				if err := copyDirectory(source, filepath.Join(temp, element)); err != nil {
					return err
				}
			}
		}
		if err := copyFile(combinePath(content, "descriptor.json"), filepath.Join(temp, "descriptor.json")); err != nil {
			return err
		}
		listed, err := allFiles(temp, -1, false)
		if err != nil {
			return err
		}
		var files []string
		for _, file := range listed {
			if !strings.HasSuffix(strings.ToLower(file), ".pdb") {
				files = append(files, file)
			}
		}
		if files, err = applyClioIgnore(files, packagePath); err != nil {
			return err
		}
		var relative []string
		for _, file := range files {
			relative = append(relative, filepath.ToSlash(relativePath(temp, file)))
		}
		if missing := foldersWithoutDescriptor(relative); len(missing) > 0 {
			base := fullPath(content)
			for index := range missing {
				missing[index].path = combineAll(base, strings.Split(missing[index].path, "/")...)
			}
			return &descriptorMissingError{folders: missing}
		}
		if err := os.Remove(packedPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return packToGzip(files, temp, packedPath)
	})
}

func combineAll(first string, rest ...string) string {
	for _, part := range rest {
		first = combinePath(first, part)
	}
	return first
}

// applyClioIgnore is PackageArchiver.ApplyClioIgnore: the workspace's .clio/clioignore (two folders above the
// package) filters the files, matched against their file:// URI. A package's own *clioignore file then adds back
// the files under its folder that its rules (after the workspace's, the first such file's lines) do not ignore —
// a second time for those already kept, as clio does.
func applyClioIgnore(files []string, packagePath string) ([]string, error) {
	workspaceIgnore := filepath.Join(filepath.Dir(filepath.Dir(filepath.Clean(packagePath))), ".clio", "clioignore")
	workspaceIgnoreExists := fileExists(workspaceIgnore)
	var packageIgnores []string
	for _, file := range files {
		if strings.HasSuffix(file, "clioignore") {
			packageIgnores = append(packageIgnores, file)
		}
	}
	if !workspaceIgnoreExists && len(packageIgnores) == 0 {
		return files, nil
	}
	if !workspaceIgnoreExists {
		return nil, errors.New("Object reference not set to an instance of an object.")
	}
	rules := &ignoreSet{}
	lines, err := readAllLines(workspaceIgnore)
	if err != nil {
		return nil, err
	}
	if err := rules.add(lines); err != nil {
		return nil, err
	}
	var kept []string
	for _, file := range files {
		if !rules.ignored(fileURI(file)) {
			kept = append(kept, file)
		}
	}
	for _, ignoreFile := range packageIgnores {
		folder := filepath.Dir(ignoreFile)
		lines, err := readAllLines(packageIgnores[0])
		if err != nil {
			return nil, err
		}
		if err := rules.add(lines); err != nil {
			return nil, err
		}
		for _, file := range files {
			if strings.HasPrefix(file, folder) && !rules.ignored(fileURI(file)) {
				kept = append(kept, file)
			}
		}
	}
	return kept, nil
}

// fileURI is new Uri(path).ToString() for a local absolute path.
func fileURI(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return "file://" + slashed
}

// packToGzip is CompressionUtilities.PackToGZip: per file, the path relative to root as an Int32 count of UTF-16
// units and the units, then the content as an Int32 length and the bytes, all little-endian, in one gzip stream.
func packToGzip(files []string, root, packedPath string) error {
	output, err := os.Create(packedPath)
	if err != nil {
		return err
	}
	writer := gzip.NewWriter(output)
	trimmedRoot := strings.TrimRight(root, string(filepath.Separator))
	for _, file := range files {
		relative := strings.TrimLeft(file[len(trimmedRoot):], string(filepath.Separator))
		units := utf16.Encode([]rune(relative))
		if err := binary.Write(writer, binary.LittleEndian, int32(len(units))); err != nil {
			output.Close()
			return err
		}
		if err := binary.Write(writer, binary.LittleEndian, units); err != nil {
			output.Close()
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			output.Close()
			return err
		}
		if err := binary.Write(writer, binary.LittleEndian, int32(len(data))); err != nil {
			output.Close()
			return err
		}
		if _, err := writer.Write(data); err != nil {
			output.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

// zipDirectory is ZipFile.CreateFromDirectory(source, destination): every file below source in enumeration
// order, named by its path relative to source, deflated.
func zipDirectory(source, destination string) error {
	if err := os.Remove(destination); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	files, err := allFiles(source, -1, false)
	if err != nil {
		return err
	}
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(output)
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			output.Close()
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			output.Close()
			return err
		}
		header.Name = filepath.ToSlash(relativePath(source, file))
		header.Method = zip.Deflate
		entry, err := archive.CreateHeader(header)
		if err != nil {
			output.Close()
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			output.Close()
			return err
		}
		if _, err := entry.Write(data); err != nil {
			output.Close()
			return err
		}
	}
	if err := archive.Close(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

// unpackGzip is CompressionUtilities.UnpackFromGZip: the entries of a clio .gz stream written below destination.
// An entry whose path would leave destination is refused (clio writes it wherever it points).
func unpackGzip(packedPath, destination string) error {
	input, err := os.Open(packedPath)
	if err != nil {
		return err
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		return gzipError(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return gzipError(err)
	}
	position := 0
	readInt := func() (int, bool) {
		if len(data)-position < 4 {
			return 0, false
		}
		value := int32(binary.LittleEndian.Uint32(data[position:]))
		position += 4
		return int(value), true
	}
	for {
		length, ok := readInt()
		if !ok || length < 0 || int64(length)*2 > int64(len(data)-position) {
			return nil
		}
		units := make([]uint16, length)
		for index := range units {
			units[index] = binary.LittleEndian.Uint16(data[position:])
			position += 2
		}
		relative := strings.ReplaceAll(string(utf16.Decode(units)), `\`, string(filepath.Separator))
		if relative == "" {
			return nil
		}
		size, _ := readInt()
		size = max(0, min(size, len(data)-position))
		content := data[position : position+size]
		position += size
		target := combinePath(destination, relative)
		if !insideFolder(destination, target) {
			return fmt.Errorf("The archive %s holds the entry '%s', which would be written outside %s.", packedPath, relative, destination)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o666); err != nil {
			return err
		}
	}
}

func insideFolder(folder, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(folder), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// gzipError is the text GZipStream raises for data that is not a gzip stream.
func gzipError(err error) error {
	if errors.Is(err, gzip.ErrHeader) || errors.Is(err, gzip.ErrChecksum) {
		return errors.New("The archive entry was compressed using an unsupported compression method.")
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return errors.New("Unexpected end of stream.")
	}
	return err
}

// unzip is ZipFile.ExtractToDirectory into a fresh folder; an entry that would leave it is refused with .NET's text.
func unzip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		if errors.Is(err, zip.ErrFormat) {
			return errors.New("End of Central Directory record could not be found.")
		}
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		target := filepath.Join(destination, filepath.FromSlash(entry.Name))
		if !insideFolder(destination, target) {
			return errors.New("Extracting Zip entry would have resulted in a file outside the specified destination directory.")
		}
		if strings.HasSuffix(entry.Name, "/") {
			if err := os.MkdirAll(target, 0o777); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
			return err
		}
		stream, err := entry.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o666); err != nil {
			return err
		}
	}
	return nil
}
