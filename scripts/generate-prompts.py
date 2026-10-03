#!/usr/bin/env python3
"""Regenerate cmd/creatio-mcp-go/knowledge_prompt_table.go from clio's live MCP prompts.

clio's prompts are compiled C# formatters. This script reproduces them as data:

1. It reads the prompt method signatures in a clio checkout to learn which arguments are bool or int.
2. It starts `clio mcp-server`, lists the prompts and renders each one with a placeholder in every
   argument, then with each optional argument left out, empty, blank, and each pair of optional
   arguments left out.
3. From the differences it derives, per argument, what an absent, empty or blank value does to the
   template, and checks that this model reproduces EVERY captured render, errors included.
4. Only when all of them match does it write the Go table (and runs gofmt on it).

    python3 scripts/generate-prompts.py --clio-dll <path>/clio.dll --clio-source <clio checkout>

Starts no Creatio connection; prompts/get needs none.
"""
import argparse, glob, itertools, json, os, pathlib, re, subprocess, sys

REPO = pathlib.Path(__file__).resolve().parent.parent
OUTPUT = REPO / "cmd/creatio-mcp-go/knowledge_prompt_table.go"
INT_SENTINEL_BASE = 7000001


def sentinel(name):
    return "«ARG:" + name + "»"


def sentinel_of(kind, index, name):
    return sentinel(name) if kind == "string" else str(INT_SENTINEL_BASE + index)


# --- 1. parameter types from clio's source -------------------------------------------------------------

def prompt_signatures(prompts_dir):
    """Maps "arg1,arg2,..." (a prompt's parameter names in order) to [(name, C# type)]."""
    signatures = {}
    for path in glob.glob(os.path.join(prompts_dir, "**", "*.cs"), recursive=True):
        source = open(path, encoding="utf-8-sig").read()
        for match in re.finditer(r'McpServerPrompt\(\s*Name\s*=\s*([^,)\]]+)', source):
            method = re.compile(r'public\s+static\s+string\s+\w+\s*\(').search(source, match.end())
            if not method:
                continue
            start = index = method.end()
            depth, in_string = 1, False
            while depth:
                character = source[index]
                if character == '"':
                    in_string = not in_string
                elif not in_string:
                    depth += character in "(["
                    depth -= character in ")]"
                index += 1
            parameters = re.sub(r'\[[^\[\]]*(\([^()]*(\([^()]*\))*[^()]*\))?[^\[\]]*\]', ' ', source[start:index - 1])
            parts, depth, current = [], 0, ""
            for character in parameters:
                depth += character in "(<"
                depth -= character in ")>"
                if character == "," and depth == 0:
                    parts.append(current)
                    current = ""
                else:
                    current += character
            parts.append(current)
            parsed = []
            for part in (p.strip() for p in parts):
                if part:
                    tokens = part.split("=", 1)[0].split()
                    parsed.append((tokens[-1], " ".join(tokens[:-1])))
            signatures.setdefault(",".join(name for name, _ in parsed), parsed)
    return signatures


# --- 2. capture from clio ------------------------------------------------------------------------------

class Clio:
    def __init__(self, dll):
        self.process = subprocess.Popen(["dotnet", dll, "mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.DEVNULL, text=True, bufsize=1, encoding="utf-8")
        self.next_id = 0
        self.server_info = self.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                                       "clientInfo": {"name": "generate-prompts", "version": "1"}})["result"]["serverInfo"]
        self.process.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}}) + "\n")
        self.process.stdin.flush()

    def request(self, method, params):
        self.next_id += 1
        self.process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": self.next_id, "method": method, "params": params}) + "\n")
        self.process.stdin.flush()
        while True:
            line = self.process.stdout.readline()
            if not line:
                raise RuntimeError(f"clio exited during {method}")
            try:
                message = json.loads(line)
            except json.JSONDecodeError:
                continue
            if message.get("id") == self.next_id:
                return message

    def render(self, name, arguments):
        response = self.request("prompts/get", {"name": name, "arguments": arguments})
        if "error" in response:
            return {"error": response["error"]}
        result = response["result"]
        message = result["messages"][0]
        return {"text": message["content"]["text"], "description": result.get("description"),
                "role": message["role"], "count": len(result["messages"])}

    def close(self):
        self.process.kill()
        self.process.wait()


def capture(clio, signatures):
    captured = []
    for prompt in clio.request("prompts/list", {})["result"]["prompts"]:
        names = [a["name"] for a in prompt.get("arguments", [])]
        types = dict(signatures.get(",".join(names), []))
        kinds = {n: "bool" if types.get(n, "string").startswith("bool") else
                 "int" if types.get(n, "string").startswith("int") else "string" for n in names}
        base = {n: sentinel_of(kinds[n], i, n) for i, n in enumerate(names) if kinds[n] != "bool"}
        renders = {"base": clio.render(prompt["name"], base)}
        optional = [a["name"] for a in prompt.get("arguments", []) if not a.get("required") and kinds[a["name"]] != "bool"]
        for n in optional:
            renders["minus|" + n] = clio.render(prompt["name"], {k: v for k, v in base.items() if k != n})
        for n in names:
            if kinds[n] == "string":
                renders["empty|" + n] = clio.render(prompt["name"], {**base, n: ""})
                renders["space|" + n] = clio.render(prompt["name"], {**base, n: "  "})
            if kinds[n] == "bool":
                renders["boolstr|" + n] = clio.render(prompt["name"], {**base, n: "true"})
        for a, b in itertools.combinations(optional, 2):
            renders["minus2|%s|%s" % (a, b)] = clio.render(prompt["name"], {k: v for k, v in base.items() if k not in (a, b)})
        if len(optional) > 2:
            renders["minusall"] = clio.render(prompt["name"], {k: v for k, v in base.items() if k not in optional})
        captured.append({"prompt": prompt, "kinds": kinds, "renders": renders})
    return captured


# --- 3. model and verification -------------------------------------------------------------------------

def region(template, variant):
    prefix, limit = 0, min(len(template), len(variant))
    while prefix < limit and template[prefix] == variant[prefix]:
        prefix += 1
    suffix = 0
    while suffix < limit - prefix and template[-1 - suffix] == variant[-1 - suffix]:
        suffix += 1
    return {"mode": "region", "start": prefix, "end": len(template) - suffix, "text": variant[prefix:len(variant) - suffix]}


def classify_absent(template, variant, placeholder):
    if variant == template.replace(placeholder, ""):
        return {"mode": "subst", "text": ""}
    count = template.count(placeholder)
    if count:
        prefix = 0
        while prefix < min(len(template), len(variant)) and template[prefix] == variant[prefix]:
            prefix += 1
        if template.find(placeholder) == prefix and (len(variant) - len(template)) % count == 0:
            length = (len(variant) - len(template)) // count + len(placeholder)
            if length >= 0 and template.replace(placeholder, variant[prefix:prefix + length]) == variant:
                return {"mode": "subst", "text": variant[prefix:prefix + length]}
    return region(template, variant)


def render_model(entry, rules, arguments):
    """The Go renderer's algorithm (knowledge_prompts.go); None means clio answers an error."""
    template = entry["renders"]["base"]["text"]
    chosen, absent = {}, set()
    for index, argument in enumerate(entry["prompt"].get("arguments", [])):
        name, kind = argument["name"], entry["kinds"][argument["name"]]
        if kind == "bool":
            if name in arguments or argument.get("required"):
                return None
            continue
        placeholder = sentinel_of(kind, index, name)
        if name not in arguments:
            if argument.get("required"):
                return None
            rule = rules[name]["absent"]
        else:
            value = arguments[name]
            if kind == "int":
                try:
                    rule = {"mode": "value", "text": str(int(value.strip()))}
                except ValueError:
                    return None
            elif value == "":
                rule = rules[name]["empty"]
            elif value.strip() == "":
                rule = rules[name]["space"]
                if rule["mode"] == "value":
                    rule = {"mode": "value", "text": value}
            else:
                rule = {"mode": "value", "text": value}
            if rule["mode"] == "absent":
                rule = rules[name]["absent"]
        if rule["mode"] == "error":
            return None
        if name not in arguments or rule is rules[name]["absent"]:
            absent.add(name)
        chosen[name] = (placeholder, rule)
    for pair in entry.get("pairs", []):
        if pair["a"] in absent and pair["b"] in absent:
            chosen[pair["a"]] = (chosen[pair["a"]][0], pair["region"])
            chosen[pair["b"]] = (chosen[pair["b"]][0], {"mode": "subst", "text": ""})
    regions, replacements = [], {}
    for placeholder, rule in chosen.values():
        if rule["mode"] in ("value", "subst"):
            replacements[placeholder] = rule["text"]
        elif rule["mode"] == "region":
            regions.append(rule)
            replacements[placeholder] = ""
    regions.sort(key=lambda r: -r["start"])
    for later, earlier in zip(regions, regions[1:]):
        if earlier["end"] > later["start"]:
            raise ValueError("overlapping regions")
    text = template
    for r in regions:
        text = text[:r["start"]] + r["text"] + text[r["end"]:]
    if not replacements:
        return text
    pattern = re.compile("|".join(re.escape(p) for p in replacements))
    return pattern.sub(lambda m: replacements[m.group(0)], text)


def build(captured):
    failures, prompts = [], []
    for entry in captured:
        prompt, renders, kinds = entry["prompt"], entry["renders"], entry["kinds"]
        arguments = prompt.get("arguments", [])
        base_ok = "text" in renders["base"]
        base = {a["name"]: sentinel_of(kinds[a["name"]], i, a["name"]) for i, a in enumerate(arguments) if kinds[a["name"]] != "bool"}
        rules = {}
        entry["pairs"] = []
        if base_ok:
            template = renders["base"]["text"]
            for index, argument in enumerate(arguments):
                name, kind = argument["name"], kinds[argument["name"]]
                if kind == "bool":
                    continue
                placeholder = sentinel_of(kind, index, name)
                missing = renders.get("minus|" + name)
                rule = {"absent": {"mode": "error"} if missing is None or "error" in missing
                        else classify_absent(template, missing["text"], placeholder)}
                for key, literal in (("empty", ""), ("space", "  ")):
                    variant = renders.get(key + "|" + name)
                    if variant is None:
                        rule[key] = {"mode": "value", "text": literal}
                    elif "error" in variant:
                        rule[key] = {"mode": "error"}
                    elif variant["text"] == template.replace(placeholder, literal):
                        rule[key] = {"mode": "value", "text": literal}
                    elif missing and "text" in missing and variant["text"] == missing["text"]:
                        rule[key] = {"mode": "absent"}
                    else:
                        rule[key] = region(template, variant["text"])
                rules[name] = rule
            for key, variant in renders.items():
                parts = key.split("|")
                if parts[0] != "minus2" or "error" in variant:
                    continue
                try:
                    got = render_model(entry, rules, {k: v for k, v in base.items() if k not in parts[1:]})
                except ValueError:
                    got = None
                if got != variant["text"]:
                    entry["pairs"].append({"a": parts[1], "b": parts[2], "region": region(template, variant["text"])})
        optional = [a["name"] for a in arguments if not a.get("required") and kinds[a["name"]] != "bool"]
        for key, variant in renders.items():
            parts = key.split("|")
            arguments_for = {
                "base": lambda: base,
                "minus": lambda: {k: v for k, v in base.items() if k != parts[1]},
                "empty": lambda: {**base, parts[1]: ""},
                "space": lambda: {**base, parts[1]: "  "},
                "minus2": lambda: {k: v for k, v in base.items() if k not in parts[1:]},
                "minusall": lambda: {k: v for k, v in base.items() if k not in optional},
                "boolstr": lambda: {**base, parts[1]: "true"},
            }[parts[0]]()
            want = None if "error" in variant else variant["text"]
            try:
                got = render_model(entry, rules, arguments_for) if base_ok else None
            except ValueError as error:
                got = "error: " + str(error)
            if got != want:
                failures.append(f"{prompt['name']} [{key}]")
            if "text" in variant and (variant["count"] != 1 or variant["role"] != "user" or variant["description"] != prompt.get("description")):
                failures.append(f"{prompt['name']} [{key}]: not one user message with the listed description")
            if "error" in variant and variant["error"].get("code") != -32603:
                failures.append(f"{prompt['name']} [{key}]: unexpected error code {variant['error'].get('code')}")
        prompts.append({"entry": entry, "rules": rules, "base_ok": base_ok})
    return prompts, failures


# --- 4. Go output ---------------------------------------------------------------------------------------

def go_file(prompts, clio_version, command):
    literal = lambda value: json.dumps(value, ensure_ascii=False)

    def rule(r, template):
        if not r:
            return "knowledgePromptRule{}"
        if r["mode"] == "region":
            to_bytes = lambda index: len(template[:index].encode("utf-8"))
            return "knowledgePromptRule{mode: \"region\", start: %d, end: %d, text: %s}" % (
                to_bytes(r["start"]), to_bytes(r["end"]), literal(r["text"]))
        return "knowledgePromptRule{mode: %s, text: %s}" % (literal(r["mode"]), literal(r.get("text", "")))

    lines = [f"// Code generated by `{command}` from clio {clio_version} prompts/list and prompts/get. DO NOT EDIT.",
             "// To regenerate after a clio update, see \"Prompt table\" in docs/parity.md.", "",
             "package main", "", "func init() {", "\tknowledgePrompts = []knowledgePrompt{"]
    for item in prompts:
        entry, prompt = item["entry"], item["entry"]["prompt"]
        template = entry["renders"]["base"].get("text", "")
        lines.append("\t\t{")
        lines.append("\t\t\tname: %s, description: %s," % (literal(prompt["name"]), literal(prompt.get("description", ""))))
        lines.append("\t\t\ttemplate: %s," % literal(template) if item["base_ok"] else "\t\t\talwaysFails: true,")
        lines.append("\t\t\targs: []knowledgePromptArg{")
        for index, argument in enumerate(prompt.get("arguments", [])):
            name, kind = argument["name"], entry["kinds"][argument["name"]]
            rules = item["rules"].get(name, {})
            lines.append("\t\t\t\t{name: %s, description: %s, required: %s, kind: %s, sentinel: %s, absent: %s, empty: %s, space: %s}," % (
                literal(name), literal(argument.get("description", "")), "true" if argument.get("required") else "false",
                literal(kind), literal(sentinel_of(kind, index, name) if kind != "bool" else ""),
                rule(rules.get("absent"), template), rule(rules.get("empty"), template), rule(rules.get("space"), template)))
        lines.append("\t\t\t},")
        if entry["pairs"]:
            pairs = ", ".join("{a: %s, b: %s, region: %s}" % (literal(p["a"]), literal(p["b"]), rule(p["region"], template))
                              for p in entry["pairs"])
            lines.append("\t\t\tpairs: []knowledgePromptPair{%s}," % pairs)
        lines.append("\t\t},")
    lines += ["\t}", "}", ""]
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--clio-dll", required=True, help="clio.dll whose prompts are reproduced")
    parser.add_argument("--clio-source", required=True, help="a clio checkout at the same version (reads prompt signatures)")
    parser.add_argument("--output", default=str(OUTPUT))
    options = parser.parse_args()
    prompts_dir = os.path.join(options.clio_source, "clio", "Command", "McpServer", "Prompts")
    if not os.path.isdir(prompts_dir):
        sys.exit(f"no prompt sources under {prompts_dir}")
    signatures = prompt_signatures(prompts_dir)
    clio = Clio(options.clio_dll)
    try:
        captured = capture(clio, signatures)
    finally:
        clio.close()
    prompts, failures = build(captured)
    if failures:
        print("the template model does not reproduce these clio renders; nothing written:", file=sys.stderr)
        for failure in failures:
            print("  " + failure, file=sys.stderr)
        return 1
    command = "python3 scripts/generate-prompts.py"
    pathlib.Path(options.output).write_text(go_file(prompts, clio.server_info.get("version", "?"), command), encoding="utf-8")
    subprocess.run(["gofmt", "-w", options.output], check=True)
    print(f"{len(prompts)} prompts, every captured render reproduced; wrote {options.output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
