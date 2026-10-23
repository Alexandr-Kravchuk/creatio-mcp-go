namespace Clio.Command.McpServer.Tools.MobilePageConverter;

using System;
using System.Collections;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Threading;
using Clio.Command.McpServer.Tools.MobileComponentRegistry;
using Newtonsoft.Json.Linq;
using JsonNode = System.Text.Json.Nodes.JsonNode;
using JsonObject = System.Text.Json.Nodes.JsonObject;

// Oracle hook for the Go port (not part of clio): records every Analyze call's inputs and its answer.
internal static class OracleRecorder {
	private static int _counter;
	private static readonly JsonSerializerOptions Plain = new() { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping };

	internal static string Dir => Environment.GetEnvironmentVariable("CLIO_ORACLE_DIR");

	internal static string TestName() {
		var trace = new StackTrace();
		foreach (StackFrame frame in trace.GetFrames()) {
			var method = frame.GetMethod();
			var type = method?.DeclaringType;
			while (type?.DeclaringType != null && type.Name.StartsWith("<")) {
				type = type.DeclaringType;
			}
			if (type?.Namespace?.StartsWith("Clio.Tests") == true) {
				string name = method.Name;
				if (name.StartsWith("<")) {
					int end = name.IndexOf('>');
					name = end > 1 ? name[1..end] : name;
				}
				return type.Name + "." + name;
			}
		}
		return "unknown";
	}

	internal static void Record(Action<Utf8JsonWriter> inputs, string output, string error) {
		string dir = Dir;
		if (string.IsNullOrEmpty(dir)) {
			return;
		}
		int n = Interlocked.Increment(ref _counter);
		string name = TestName();
		using var stream = new MemoryStream();
		using (var writer = new Utf8JsonWriter(stream, new JsonWriterOptions { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping, Indented = false })) {
			writer.WriteStartObject();
			writer.WriteString("name", name);
			writer.WritePropertyName("inputs");
			inputs(writer);
			if (output != null) {
				writer.WriteString("output", output);
			}
			if (error != null) {
				writer.WriteString("error", error);
			}
			writer.WriteEndObject();
		}
		string safe = new string(name.Select(c => char.IsLetterOrDigit(c) || c == '.' ? c : '_').ToArray());
		File.WriteAllBytes(Path.Combine(dir, $"{Environment.ProcessId}-{n:D5}-{safe}.json"), stream.ToArray());
	}

	internal static void Node(Utf8JsonWriter w, string name, JsonNode node) {
		w.WritePropertyName(name);
		if (node is null) {
			w.WriteNullValue();
		} else {
			node.WriteTo(w);
		}
	}

	internal static void Token(Utf8JsonWriter w, string name, JToken token) {
		w.WritePropertyName(name);
		if (token is null) {
			w.WriteNullValue();
		} else {
			w.WriteRawValue(token.ToString(Newtonsoft.Json.Formatting.None), skipInputValidation: true);
		}
	}

	internal static void Obj(Utf8JsonWriter w, string name, object value) {
		w.WritePropertyName(name);
		if (value is null) {
			w.WriteNullValue();
		} else {
			w.WriteRawValue(JsonSerializer.Serialize(value, value.GetType(), Plain), skipInputValidation: true);
		}
	}

	private static object Comparer(object collection) {
		var prop = collection?.GetType().GetProperty("Comparer");
		object comparer = prop?.GetValue(collection);
		if (comparer is null) {
			return collection is null ? null : "none:" + collection.GetType().Name;
		}
		if (ReferenceEquals(comparer, StringComparer.OrdinalIgnoreCase)) {
			return true;
		}
		if (ReferenceEquals(comparer, StringComparer.Ordinal) || comparer.GetType().Name.Contains("Ordinal")
			|| comparer.GetType().Name.Contains("GenericEqualityComparer") || comparer.GetType().Name.Contains("NonRandomized")
			|| comparer.GetType().Name.Contains("Randomized")) {
			return comparer.GetType().Name.Contains("IgnoreCase") ? true : false;
		}
		return "other:" + comparer.GetType().FullName;
	}

	private static void WriteComparer(Utf8JsonWriter w, object collection) {
		object c = Comparer(collection);
		w.WritePropertyName("ci");
		switch (c) {
			case bool b: w.WriteBooleanValue(b); break;
			case string s: w.WriteStringValue(s); break;
			default: w.WriteNullValue(); break;
		}
	}

	internal static void Set(Utf8JsonWriter w, string name, IEnumerable<string> set) {
		w.WritePropertyName(name);
		if (set is null) {
			w.WriteNullValue();
			return;
		}
		w.WriteStartObject();
		WriteComparer(w, set);
		w.WriteStartArray("items");
		foreach (string item in set) {
			w.WriteStringValue(item);
		}
		w.WriteEndArray();
		w.WriteEndObject();
	}

	internal static void Dict<TValue>(Utf8JsonWriter w, string name, IEnumerable<KeyValuePair<string, TValue>> dict,
		Action<Utf8JsonWriter, TValue> value) {
		w.WritePropertyName(name);
		if (dict is null) {
			w.WriteNullValue();
			return;
		}
		w.WriteStartObject();
		WriteComparer(w, dict);
		w.WriteStartArray("entries");
		foreach (KeyValuePair<string, TValue> pair in dict) {
			w.WriteStartArray();
			w.WriteStringValue(pair.Key);
			value(w, pair.Value);
			w.WriteEndArray();
		}
		w.WriteEndArray();
		w.WriteEndObject();
	}

	internal static void Str(Utf8JsonWriter w, string value) {
		if (value is null) {
			w.WriteNullValue();
		} else {
			w.WriteStringValue(value);
		}
	}

	internal static void Element(Utf8JsonWriter w, JsonElement? value) {
		if (value is null) {
			w.WriteNullValue();
		} else {
			value.Value.WriteTo(w);
		}
	}

	internal static void Entry(Utf8JsonWriter w, ComponentRegistryEntry e) {
		if (e is null) {
			w.WriteNullValue();
			return;
		}
		w.WriteStartObject();
		w.WritePropertyName("componentType"); Str(w, e.ComponentType);
		w.WritePropertyName("container");
		if (e.Container is null) { w.WriteNullValue(); } else { w.WriteBooleanValue(e.Container.Value); }
		w.WritePropertyName("description"); Str(w, e.Description);
		w.WritePropertyName("example"); Element(w, e.Example);
		w.WritePropertyName("designerDefaults"); Element(w, e.DesignerDefaults);
		Dict(w, "properties", e.Properties, (ww, p) => {
			if (p is null) { ww.WriteNullValue(); return; }
			ww.WriteStartObject();
			ww.WritePropertyName("type"); Str(ww, p.Type);
			ww.WritePropertyName("default"); Element(ww, p.Default);
			ww.WriteEndObject();
		});
		Dict(w, "inputs", e.Inputs, (ww, v) => v.WriteTo(ww));
		Dict(w, "outputs", e.Outputs, (ww, v) => v.WriteTo(ww));
		w.WriteEndObject();
	}
}
