namespace Clio.Command.McpServer.Tools.MobilePageConverter;

using System;
using System.Collections.Generic;
using System.Text.Json;
using Clio.Command.McpServer.Tools.MobileComponentRegistry;
using Newtonsoft.Json.Linq;
using JsonNode = System.Text.Json.Nodes.JsonNode;
using JsonObject = System.Text.Json.Nodes.JsonObject;

public static partial class WebToMobileAnalysisService {
	public static MobilePageConversionGuide Analyze(
		PageBundleInfo bundle,
		IReadOnlySet<string> mobileTypes,
		IReadOnlySet<string> webTypes,
		IReadOnlyDictionary<string, ComponentRegistryEntry> webByType,
		IReadOnlyDictionary<string, ComponentRegistryEntry> mobileByType,
		WebToMobilePageConversionRules rules,
		TemplateMappingRule templateRule,
		IReadOnlySet<string> mobileRequestTypes,
		string sourcePage,
		string sourceTemplate,
		string suggestedTarget,
		IReadOnlyDictionary<string, string> containerNameMap,
		SectionRegistrationInfo sectionRegistration = null,
		PageBusinessRuleProbeResult pageBusinessRulesProbe = null,
		IReadOnlySet<string> templateComponentNames = null,
		IReadOnlyDictionary<string, ComponentMappingRule> componentNameMap = null,
		IReadOnlyList<PositionalPlacement> positionalPlacements = null,
		IReadOnlyDictionary<string, string> mobileContainerParents = null,
		JsonNode mobileTemplateViewModelConfig = null,
		JsonNode mobileTemplateModelConfig = null,
		IReadOnlyDictionary<string, string> mobileTemplateTypesByName = null,
		IReadOnlyDictionary<string, JsonObject> mobileTemplateLayoutConfigs = null,
		IReadOnlyDictionary<string, JObject> mobileTemplateNodesByName = null,
		IReadOnlyDictionary<string, JObject> webTemplateBaselineNodes = null,
		JObject webTemplateResources = null,
		MobileActionTargetProbeResult actionTargetsProbe = null,
		IReadOnlyList<ExistingMobilePageInfo> existingMobilePages = null,
		MobileRegistryGeneration mobileRegistryGeneration = null) {
		if (string.IsNullOrEmpty(OracleRecorder.Dir)) {
			return AnalyzeCore(bundle, mobileTypes, webTypes, webByType, mobileByType, rules, templateRule,
				mobileRequestTypes, sourcePage, sourceTemplate, suggestedTarget, containerNameMap, sectionRegistration,
				pageBusinessRulesProbe, templateComponentNames, componentNameMap, positionalPlacements,
				mobileContainerParents, mobileTemplateViewModelConfig, mobileTemplateModelConfig,
				mobileTemplateTypesByName, mobileTemplateLayoutConfigs, mobileTemplateNodesByName,
				webTemplateBaselineNodes, webTemplateResources, actionTargetsProbe, existingMobilePages,
				mobileRegistryGeneration);
		}
		// Inputs are written BEFORE the call: the analysis mutates the bundle it is given.
		var buffer = new System.IO.MemoryStream();
		using (var w = new Utf8JsonWriter(buffer)) {
			w.WriteStartObject();
			if (bundle is null) {
				w.WriteNull("bundle");
			} else {
				w.WriteStartObject("bundle");
				OracleRecorder.Node(w, "viewConfig", bundle.ViewConfig);
				OracleRecorder.Node(w, "viewModelConfig", bundle.ViewModelConfig);
				OracleRecorder.Node(w, "modelConfig", bundle.ModelConfig);
				w.WriteBoolean("resourcesNull", bundle.Resources is null);
				OracleRecorder.Node(w, "resourcesStrings", bundle.Resources?.Strings);
				w.WritePropertyName("handlers"); OracleRecorder.Str(w, bundle.Handlers);
				w.WritePropertyName("converters"); OracleRecorder.Str(w, bundle.Converters);
				w.WritePropertyName("validators"); OracleRecorder.Str(w, bundle.Validators);
				w.WriteEndObject();
			}
			OracleRecorder.Set(w, "mobileTypes", mobileTypes);
			OracleRecorder.Set(w, "webTypes", webTypes);
			OracleRecorder.Dict(w, "webByType", webByType, OracleRecorder.Entry);
			OracleRecorder.Dict(w, "mobileByType", mobileByType, OracleRecorder.Entry);
			OracleRecorder.Obj(w, "rules", rules);
			OracleRecorder.Obj(w, "templateRule", templateRule);
			OracleRecorder.Set(w, "mobileRequestTypes", mobileRequestTypes);
			w.WritePropertyName("sourcePage"); OracleRecorder.Str(w, sourcePage);
			w.WritePropertyName("sourceTemplate"); OracleRecorder.Str(w, sourceTemplate);
			w.WritePropertyName("suggestedTarget"); OracleRecorder.Str(w, suggestedTarget);
			OracleRecorder.Dict(w, "containerNameMap", containerNameMap, OracleRecorder.Str);
			w.WritePropertyName("sectionRegistration");
			if (sectionRegistration is null) { w.WriteNullValue(); }
			else { w.WriteRawValue(JsonSerializer.Serialize(sectionRegistration, BindingsModule.CreateMcpSerializerOptions()), true); }
			w.WritePropertyName("pageBusinessRulesProbe");
			if (pageBusinessRulesProbe is null) { w.WriteNullValue(); }
			else {
				w.WriteStartObject();
				w.WriteBoolean("probeOk", pageBusinessRulesProbe.ProbeOk);
				w.WritePropertyName("note"); OracleRecorder.Str(w, pageBusinessRulesProbe.Note);
				w.WritePropertyName("rules");
				if (pageBusinessRulesProbe.Rules is null) { w.WriteNullValue(); }
				else {
					w.WriteStartArray();
					foreach (SourcePageBusinessRule rule in pageBusinessRulesProbe.Rules) {
						if (rule is null) { w.WriteNullValue(); continue; }
						w.WriteStartObject();
						w.WritePropertyName("caption"); OracleRecorder.Str(w, rule.Caption);
						w.WriteNumber("conditionIssue", (int)rule.ConditionIssue);
						OracleRecorder.Node(w, "condition", rule.Condition);
						w.WritePropertyName("actions");
						if (rule.Actions is null) { w.WriteNullValue(); }
						else {
							w.WriteStartArray();
							foreach (SourcePageRuleAction action in rule.Actions) {
								if (action is null) { w.WriteNullValue(); continue; }
								w.WriteStartObject();
								w.WritePropertyName("actionType"); OracleRecorder.Str(w, action.ActionType);
								w.WritePropertyName("elementItems");
								if (action.ElementItems is null) { w.WriteNullValue(); }
								else {
									w.WriteStartArray();
									foreach (string item in action.ElementItems) { OracleRecorder.Str(w, item); }
									w.WriteEndArray();
								}
								w.WriteEndObject();
							}
							w.WriteEndArray();
						}
						w.WriteEndObject();
					}
					w.WriteEndArray();
				}
				w.WriteEndObject();
			}
			OracleRecorder.Set(w, "templateComponentNames", templateComponentNames);
			OracleRecorder.Dict(w, "componentNameMap", componentNameMap, (ww, v) => {
				if (v is null) { ww.WriteNullValue(); } else { ww.WriteRawValue(JsonSerializer.Serialize(v), true); }
			});
			w.WritePropertyName("positionalPlacements");
			if (positionalPlacements is null) { w.WriteNullValue(); }
			else {
				w.WriteStartArray();
				foreach (PositionalPlacement p in positionalPlacements) {
					if (p is null) { w.WriteNullValue(); continue; }
					w.WriteStartObject();
					w.WritePropertyName("webAnchor"); OracleRecorder.Str(w, p.WebAnchor);
					w.WritePropertyName("mobileAnchor"); OracleRecorder.Str(w, p.MobileAnchor);
					w.WriteEndObject();
				}
				w.WriteEndArray();
			}
			OracleRecorder.Dict(w, "mobileContainerParents", mobileContainerParents, OracleRecorder.Str);
			OracleRecorder.Node(w, "mobileTemplateViewModelConfig", mobileTemplateViewModelConfig);
			OracleRecorder.Node(w, "mobileTemplateModelConfig", mobileTemplateModelConfig);
			OracleRecorder.Dict(w, "mobileTemplateTypesByName", mobileTemplateTypesByName, OracleRecorder.Str);
			OracleRecorder.Dict(w, "mobileTemplateLayoutConfigs", mobileTemplateLayoutConfigs, (ww, v) => {
				if (v is null) { ww.WriteNullValue(); } else { v.WriteTo(ww); }
			});
			OracleRecorder.Dict(w, "mobileTemplateNodesByName", mobileTemplateNodesByName, (ww, v) => {
				if (v is null) { ww.WriteNullValue(); } else { ww.WriteRawValue(v.ToString(Newtonsoft.Json.Formatting.None), true); }
			});
			OracleRecorder.Dict(w, "webTemplateBaselineNodes", webTemplateBaselineNodes, (ww, v) => {
				if (v is null) { ww.WriteNullValue(); } else { ww.WriteRawValue(v.ToString(Newtonsoft.Json.Formatting.None), true); }
			});
			OracleRecorder.Token(w, "webTemplateResources", webTemplateResources);
			w.WritePropertyName("actionTargetsProbe");
			if (actionTargetsProbe is null) { w.WriteNullValue(); }
			else {
				w.WriteStartObject();
				w.WriteBoolean("probeOk", actionTargetsProbe.ProbeOk);
				w.WritePropertyName("note"); OracleRecorder.Str(w, actionTargetsProbe.Note);
				OracleRecorder.Dict(w, "targetsByKey", actionTargetsProbe.TargetsByKey, (ww, v) => {
					if (v is null) { ww.WriteNullValue(); return; }
					ww.WriteStartObject();
					ww.WritePropertyName("kind"); OracleRecorder.Str(ww, v.Kind);
					ww.WritePropertyName("target"); OracleRecorder.Str(ww, v.Target);
					ww.WriteNumber("state", (int)v.State);
					ww.WritePropertyName("resolvedCandidateSchemaName"); OracleRecorder.Str(ww, v.ResolvedCandidateSchemaName);
					ww.WriteEndObject();
				});
				w.WriteEndObject();
			}
			w.WritePropertyName("existingMobilePages");
			if (existingMobilePages is null) { w.WriteNullValue(); }
			else { w.WriteRawValue(JsonSerializer.Serialize(existingMobilePages, BindingsModule.CreateMcpSerializerOptions()), true); }
			w.WritePropertyName("mobileRegistryGeneration");
			if (mobileRegistryGeneration is null) { w.WriteNullValue(); }
			else {
				w.WriteStartObject();
				OracleRecorder.Dict(w, "baseInputs", mobileRegistryGeneration.BaseInputs, (ww, v) => v.WriteTo(ww));
				w.WriteEndObject();
			}
			w.WriteEndObject();
		}
		byte[] inputs = buffer.ToArray();
		try {
			MobilePageConversionGuide guide = AnalyzeCore(bundle, mobileTypes, webTypes, webByType, mobileByType, rules,
				templateRule, mobileRequestTypes, sourcePage, sourceTemplate, suggestedTarget, containerNameMap,
				sectionRegistration, pageBusinessRulesProbe, templateComponentNames, componentNameMap,
				positionalPlacements, mobileContainerParents, mobileTemplateViewModelConfig, mobileTemplateModelConfig,
				mobileTemplateTypesByName, mobileTemplateLayoutConfigs, mobileTemplateNodesByName,
				webTemplateBaselineNodes, webTemplateResources, actionTargetsProbe, existingMobilePages,
				mobileRegistryGeneration);
			OracleRecorder.Record(w => w.WriteRawValue(inputs, true),
				JsonSerializer.Serialize(guide, BindingsModule.CreateMcpSerializerOptions()), null);
			return guide;
		} catch (Exception ex) {
			OracleRecorder.Record(w => w.WriteRawValue(inputs, true), null, ex.GetType().Name + ": " + ex.Message);
			throw;
		}
	}
}
