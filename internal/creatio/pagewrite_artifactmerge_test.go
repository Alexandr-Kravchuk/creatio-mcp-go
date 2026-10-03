package creatio

import "testing"

func TestMergeCreatioArtifactClassifiesAsClio(t *testing.T) {
	descriptor := func(manager string) *string {
		text := `{"Descriptor":{"UId":"4c5d6e7f-0000-4000-8000-000000000001","Name":"UsrX","ManagerName":"` + manager + `"}}`
		return &text
	}
	metadata := `{"MetaData":{"Schema":{"UId":"4C5D6E7F-0000-4000-8000-000000000001","A2":"UsrX"}}}`
	flat := "= MetaData.Schema.UId \"4c5d6e7f-0000-4000-8000-000000000001\"\n= MetaData.Schema.A2 \"UsrX\"\n"
	cases := []struct {
		request          ArtifactMergeRequest
		status, kind, di string
	}{
		{ArtifactMergeRequest{ArtifactPath: "../x.json", BaseContent: "a", OursContent: "b", TheirsContent: "c"},
			"invalid-input", "unknown-artifact", "artifact-path must be a safe repository-relative path."},
		{ArtifactMergeRequest{ArtifactPath: "P/x.json", BaseContent: "a", OursContent: "", TheirsContent: "c"},
			"invalid-input", "unknown-artifact", "base-content, ours-content, and theirs-content are required."},
		{ArtifactMergeRequest{ArtifactPath: `P\S\X.cs`, BaseContent: "a", OursContent: "b", TheirsContent: "c"},
			"not-implemented", "csharp-source", "Merge for csharp-source is not implemented yet."},
		{ArtifactMergeRequest{ArtifactPath: "P/R/X.Process/resource.en-US.xml", BaseContent: "a", OursContent: "b", TheirsContent: "c"},
			"not-implemented", "process-resource", "Merge for process-resource is not implemented yet."},
		{ArtifactMergeRequest{ArtifactPath: "P/readme.md", BaseContent: "a", OursContent: "b", TheirsContent: "c"},
			"unsupported", "unknown-artifact", "The artifact path is not a supported Creatio merge shape."},
		{ArtifactMergeRequest{ArtifactPath: "P/D/data.en-US.json", BaseContent: "a", OursContent: "b", TheirsContent: "c"},
			"invalid-input", "data-binding", "A valid, marker-free descriptor-content is required for data-binding merge."},
		{ArtifactMergeRequest{ArtifactPath: "P/S/X/metadata.json", BaseContent: metadata, OursContent: flat, TheirsContent: metadata,
			DescriptorContent: descriptor("EntitySchemaManager")}, "not-implemented", "entity-schema-metadata", "Merge for entity-schema-metadata is not implemented yet."},
		{ArtifactMergeRequest{ArtifactPath: "P/S/X/metadata.json", BaseContent: metadata, OursContent: metadata, TheirsContent: metadata,
			DescriptorContent: descriptor("ProcessSchemaManager")}, "not-implemented", "process-schema-metadata", "Merge for process-schema-metadata is not implemented yet."},
		{ArtifactMergeRequest{ArtifactPath: "P/S/X/metadata.json", BaseContent: metadata, OursContent: `{"MetaData":{"Schema":{"UId":"x","A2":"UsrX"}}}`,
			TheirsContent: metadata, DescriptorContent: descriptor("EntitySchemaManager")}, "invalid-input", "unknown-schema-metadata", "Metadata identity does not match descriptor-content."},
	}
	for _, item := range cases {
		result := MergeCreatioArtifact(item.request, "test")
		if result.Status != item.status || result.ArtifactKind != item.kind || len(result.Diagnostics) != 1 || result.Diagnostics[0] != item.di ||
			result.Content != nil || result.ResolverVersion != "test" || result.Report.TrueConflicts == nil {
			t.Errorf("%s: %#v", item.request.ArtifactPath, result)
		}
	}
}
