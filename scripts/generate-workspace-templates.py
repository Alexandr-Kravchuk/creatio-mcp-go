#!/usr/bin/env python3
"""Regenerate internal/workspace/templates/ from the templates clio ships next to clio.dll.

clio's workspace and code-generation commands copy files from `tpl/` in the tool's install folder
(WorkingDirectoriesProvider.TemplateDirectory): they are plain files there, not embedded resources. This
script copies the ones those commands read into one deterministic zip, which the Go server embeds, plus a
manifest of every file's path, size, SHA-256 and Unix mode bits (clio's File.Copy keeps the mode on Unix).
A zip rather than a directory keeps dotfiles (`.clio`, `.mcp.json`) embeddable and keeps Git from changing
line endings in the template texts on a Windows checkout.

    python3 scripts/generate-workspace-templates.py --clio-dll <path>/clio.dll

Reads nothing else and starts nothing.
"""
import argparse, hashlib, json, os, pathlib, stat, sys, zipfile

REPO = pathlib.Path(__file__).resolve().parent.parent
OUTPUT = REPO / "internal/workspace/templates"

# What the ported tools read under tpl/ (clio 8.1.0.142):
#   workspace                     WorkspaceCreator.Create, SolutionCreator (workspace/MainSolution.slnx)
#   set-environment.cmd.tpl       EnvironmentScriptCreator
#   package, package-localization PackageCreator
#   schemas-template              SchemaBuilder (add-package as-app)
#   UnitTest.csproj.tpl, BaseComposableAppTestFixture.cs.tpl, UnitTestLibs   CreateTestProjectCommand
#   IntegrationTestProject        CreateIntegrationTestProjectCommand
#   ui, ui-project, ui-project-Empty, esproj.tpl                          UiProjectCreator
TEMPLATES = ["workspace", "set-environment.cmd.tpl", "package", "package-localization", "schemas-template",
             "UnitTest.csproj.tpl", "BaseComposableAppTestFixture.cs.tpl", "UnitTestLibs",
             "IntegrationTestProject", "ui", "ui-project", "ui-project-Empty", "esproj.tpl"]

FIXED_TIME = (2026, 1, 1, 0, 0, 0)


def collect(tpl):
    files = []
    for name in TEMPLATES:
        root = tpl / name
        if root.is_file():
            files.append(root)
        elif root.is_dir():
            files += [path for path in root.rglob("*") if path.is_file()]
        else:
            sys.exit(f"missing template {root}")
    return sorted(files, key=lambda path: path.relative_to(tpl).as_posix())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--clio-dll", required=True)
    args = parser.parse_args()
    tpl = pathlib.Path(args.clio_dll).resolve().parent / "tpl"
    if not tpl.is_dir():
        sys.exit(f"{tpl} not found")
    OUTPUT.mkdir(parents=True, exist_ok=True)
    manifest = []
    archive_path = OUTPUT / "templates.zip"
    with zipfile.ZipFile(archive_path, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for path in collect(tpl):
            relative = path.relative_to(tpl).as_posix()
            data = path.read_bytes()
            mode = stat.S_IMODE(path.stat().st_mode)
            info = zipfile.ZipInfo(relative, FIXED_TIME)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (stat.S_IFREG | mode) << 16
            archive.writestr(info, data)
            manifest.append({"path": relative, "size": len(data), "sha256": hashlib.sha256(data).hexdigest(),
                             "mode": f"{mode:04o}"})
    clio_version = pathlib.Path(args.clio_dll).resolve().parts
    version = next((part for part in reversed(clio_version) if part.count(".") == 3 and part[0].isdigit()), "unknown")
    (OUTPUT / "manifest.json").write_text(json.dumps({"clio": version, "files": manifest}, indent=1) + "\n",
                                          encoding="utf-8")
    print(f"{len(manifest)} files, {archive_path.stat().st_size} bytes, clio {version}")


if __name__ == "__main__":
    main()
