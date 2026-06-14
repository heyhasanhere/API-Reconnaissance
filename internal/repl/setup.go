package repl

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CheckPlaywrightSetup verifies that node_modules/playwright is
// available. If not, prints a one-line install instruction.
//
// We do NOT auto-install. The user said don't be clever — just tell
// them what to do.
func CheckPlaywrightSetup(out, errOut io.Writer) {
	if findPlaywright() {
		return
	}
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Playwright is not installed. The 'watch' and 'click' actions need it.")
	fmt.Fprintln(out, "Install with:")
	fmt.Fprintln(out, "  npm install && npx playwright install chromium")
	fmt.Fprintln(out, "")
}

// findPlaywright returns true if playwright is resolvable from the
// current working directory or any parent up to the project root.
// We look for node_modules/playwright by walking up.
func findPlaywright() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, "node_modules", "playwright", "package.json")
		if _, err := os.Stat(candidate); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Also check the system PATH.
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p == "" {
			continue
		}
		candidate := filepath.Join(p, "playwright")
		if _, err := os.Stat(candidate); err == nil {
			return true
		}
	}
	return false
}
