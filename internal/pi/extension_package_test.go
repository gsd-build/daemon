package pi

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPiExtensionPackageIncludesRuntimeImports(t *testing.T) {
	_, file, _, ok := runtimeCaller()
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	outDir := t.TempDir()

	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "package-pi-extension.sh"), "v0.0.0-test", outDir)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package pi extension: %v\n%s", err, output)
	}

	archivePath := filepath.Join(outDir, "gsd-cloud-pi-extension-v0.0.0-test.tar.gz")
	files := readTarGzFileSet(t, archivePath)

	required := []string{
		"package.json",
		"package-lock.json",
		"index.ts",
		"ask-user-questions.js",
		"schema-to-zod.js",
		"usage-estimator.js",
		"codex-appserver-provider.ts",
		"openrouter-provider.ts",
		"gsd-browser-skill/SKILL.md",
	}
	for _, name := range required {
		if !files[name] {
			t.Fatalf("extension archive missing %s", name)
		}
	}
	for name := range files {
		if strings.Contains(name, ".test.") || strings.HasSuffix(name, ".mjs") || strings.HasPrefix(name, "node_modules/") {
			t.Fatalf("extension archive includes non-runtime file %s", name)
		}
	}

	indexPath := filepath.Join(repoRoot, "internal", "pi", "extension", "index.ts")
	indexSource, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index.ts: %v", err)
	}
	for _, importPath := range localRuntimeImports(string(indexSource)) {
		if archiveContainsImport(files, importPath) {
			continue
		}
		t.Fatalf("extension archive missing local import %s", importPath)
	}
}

func readTarGzFileSet(t *testing.T, archivePath string) map[string]bool {
	t.Helper()

	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()

	files := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("read tar: %v", err)
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			files[strings.TrimPrefix(header.Name, "./")] = true
		}
	}
	return files
}

func localRuntimeImports(source string) []string {
	re := regexp.MustCompile(`(?m)^\s*import\s+(?:[^"']+\s+from\s+)?["']\./([^"']+)["']`)
	matches := re.FindAllStringSubmatch(source, -1)
	imports := make([]string, 0, len(matches))
	for _, match := range matches {
		imports = append(imports, match[1])
	}
	return imports
}

func archiveContainsImport(files map[string]bool, importPath string) bool {
	if files[importPath] {
		return true
	}
	if strings.HasSuffix(importPath, ".js") {
		return files[strings.TrimSuffix(importPath, ".js")+".ts"]
	}
	return false
}

func runtimeCaller() (uintptr, string, int, bool) {
	return runtime.Caller(0)
}
