package manage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func nodeBinsVerified(i audit.Item, p PlanItem, bins map[string]string) bool {
	if len(bins) == 0 {
		return true
	}
	bin := p.Binding.ResolvedTarget.Node.Bin
	if !filepath.IsAbs(bin) || filepath.Clean(bin) != bin || bin == "/" {
		return false
	}
	root, err := filepath.EvalSymlinks(i.Path)
	if err != nil || i.Owner != "pnpm" && root != p.Binding.RealPath {
		return false
	}
	for name, path := range bins {
		if !simpleName.MatchString(name) || filepath.IsAbs(path) || !under(filepath.Join(i.Path, path), i.Path) {
			return false
		}
		payload, err := filepath.EvalSymlinks(filepath.Join(i.Path, path))
		if err != nil || !under(payload, root) || !nodeExecutable(payload) {
			return false
		}
		entry := filepath.Join(bin, name)
		if !nodeExecutable(entry) {
			return false
		}
		resolved, err := filepath.EvalSymlinks(entry)
		if err != nil || resolved != payload && (i.Owner != "pnpm" || !pnpmShimMatches(entry, payload)) {
			return false
		}
	}
	return true
}

// Recognize the macOS cmd-shim 7.0.0 templates used by pnpm 10.18.3,
// not arbitrary shell scripts or a target path mentioned in a comment.
const pnpmShimHeader = "#!/bin/sh\n" +
	`basedir=$(dirname "$(echo "$0" | sed -e 's,\\,/,g')")` + "\n\n" +
	"case `uname` in\n" +
	"    *CYGWIN*|*MINGW*|*MSYS*)\n" +
	"        if command -v cygpath > /dev/null 2>&1; then\n" +
	"            basedir=`cygpath -w \"$basedir\"`\n" +
	"        fi\n    ;;\nesac\n\n"

var pnpmShimNodePath = regexp.MustCompile(`^if \[ -z "\$NODE_PATH" \]; then
  export NODE_PATH="([^"$\\\x60\r\n]+)"
else
  export NODE_PATH="([^"$\\\x60\r\n]+):\$NODE_PATH"
fi
`)
var pnpmShimNode = regexp.MustCompile(`^if \[ -x "\$basedir/node" \]; then
  exec "\$basedir/node"  "\$basedir/([^"$\\\x60\r\n]+)" "\$@"
else
  exec node  "\$basedir/([^"$\\\x60\r\n]+)" "\$@"
fi
$`)
var pnpmShimDirect = regexp.MustCompile(`^"\$basedir/([^"$\\\x60\r\n]+)"   "\$@"
exit \$\?
$`)

func pnpmShimMatches(entry, payload string) bool {
	// The shim's echo/sed basedir calculation can rewrite these characters.
	for _, c := range filepath.Dir(entry) {
		if c == '\\' || c < 32 || c == 127 {
			return false
		}
	}
	st, err := os.Lstat(entry)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 16<<10 {
		return false
	}
	data, err := readMetadata(entry)
	if err != nil {
		return false
	}
	body, ok := strings.CutPrefix(string(data), pnpmShimHeader)
	if !ok {
		return false
	}
	if match := pnpmShimNodePath.FindStringSubmatch(body); match != nil {
		if match[1] != match[2] {
			return false
		}
		body = strings.TrimPrefix(body, match[0])
	}
	var target string
	if match := pnpmShimNode.FindStringSubmatch(body); match != nil && match[1] == match[2] {
		target = match[1]
	} else if match := pnpmShimDirect.FindStringSubmatch(body); match != nil {
		target = match[1]
	} else {
		return false
	}
	// Preserve .. until symlink traversal, as the shell does for $basedir/target.
	resolved, err := filepath.EvalSymlinks(filepath.Dir(entry) + string(filepath.Separator) + target)
	return err == nil && resolved == payload
}

func nodeExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 && syscall.Access(path, 1) == nil
}
