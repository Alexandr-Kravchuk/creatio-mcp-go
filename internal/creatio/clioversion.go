package creatio

// ClioVersion is the clio release this server reproduces, written where clio writes its own assembly
// version (export-schema's descriptor.json: ExportSchemaCommand.BuildDescriptor reads
// Assembly.GetExecutingAssembly().GetName().Version, which is 8.1.0.142 in the released clio.dll).
const ClioVersion = "8.1.0.142"
