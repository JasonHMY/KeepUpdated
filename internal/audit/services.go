package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type launchState struct {
	running bool
	exit    string
}

func parseLaunchList(raw string) (map[string]launchState, bool) {
	states := map[string]launchState{}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 0 || strings.Join(strings.Fields(lines[0]), " ") != "PID Status Label" {
		return states, false
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return states, false
		}
		if _, err := strconv.Atoi(fields[1]); err != nil {
			return states, false
		}
		running := fields[0] != "-"
		if running {
			pid, err := strconv.Atoi(fields[0])
			if err != nil || pid <= 0 {
				return states, false
			}
		}
		states[fields[2]] = launchState{running, fields[1]}
	}
	return states, true
}
func (s *Scanner) services() {
	states := map[string]launchState{}
	known := false
	if b, err := s.run("/bin/launchctl", "list"); err == nil {
		states, known = parseLaunchList(string(b))
	}
	if !known {
		s.diag("launchd", "", "user_service_query_failed")
	}
	for _, root := range []struct {
		path, owner string
		userDomain  bool
	}{{filepath.Join(s.Config.Home, "Library/LaunchAgents"), "user-launchagent", true}, {"/Library/LaunchAgents", "system-launchagent", true}, {"/Library/LaunchDaemons", "system-daemon", false}} {
		for _, d := range s.entries("launchd", root.path) {
			if !strings.HasSuffix(d.Name(), ".plist") {
				continue
			}
			p := filepath.Join(root.path, d.Name())
			i := item("service", strings.TrimSuffix(d.Name(), ".plist"), p, root.owner)
			i.Source = "launchd"
			i.Update = result("unsupported", "service_not_update_target")
			if _, err := readSmall(p); err != nil {
				i.Health = result("error", "service_definition_unreadable")
				s.add(i)
				continue
			}
			var metadata map[string]any
			b, err := s.run("/usr/bin/plutil", "-convert", "json", "-o", "-", p)
			if err != nil || json.Unmarshal(b, &metadata) != nil {
				i.Health = result("error", "service_definition_unreadable")
				s.add(i)
				continue
			}
			applyServiceMetadata(&i, metadata, states, known && root.userDomain)
			s.add(i)
		}
	}
}
func applyServiceMetadata(i *Item, m map[string]any, states map[string]launchState, known bool) {
	label := str(m["Label"])
	if label == "" {
		i.Health = result("error", "service_label_missing")
		return
	}
	i.Name = label
	i.Identity = label
	i.Health = result("unknown", "service_state_not_observed")
	if known {
		i.ActivityKnown = true
		if state, loaded := states[label]; loaded {
			i.Active = state.running
			if state.running {
				i.Health = result("ok", "service_running")
			} else {
				i.Health = result("ok", "service_loaded_not_running")
			}
			i.Evidence = append(i.Evidence, "last-exit-status:"+state.exit+" (historical)")
		} else {
			i.Health = result("ok", "service_not_loaded")
		}
	}
	if disabled, _ := m["Disabled"].(bool); disabled {
		i.Evidence = append(i.Evidence, "plist-disabled:true (runtime overrides not inspected)")
	}
	program := str(m["Program"])
	if program == "" {
		args := stringList(m["ProgramArguments"])
		if len(args) > 0 {
			program = args[0]
		}
	}
	if filepath.IsAbs(program) {
		if st, err := os.Stat(program); err != nil || !st.Mode().IsRegular() || syscall.Access(program, 1) != nil {
			i.Evidence = append(i.Evidence, "service-executable:missing-or-inaccessible")
			if i.Active {
				i.Health = result("unknown", "running_service_path_changed")
			} else {
				i.Health = result("unknown", "service_executable_unavailable")
			}
		} else {
			i.RealPath = real(program)
			i.Evidence = append(i.Evidence, "service-executable:resolved-not-started")
		}
	} else if program != "" {
		i.Evidence = append(i.Evidence, "service-executable:relative-path-not-evaluated")
	}
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		p := str(m[key])
		if !filepath.IsAbs(p) {
			continue
		}
		st, err := os.Stat(p)
		if err == nil && st.Mode().IsRegular() {
			i.Evidence = append(i.Evidence, "log-size:"+key+"="+strconv.FormatInt(st.Size(), 10))
		} else if err != nil && !os.IsNotExist(err) {
			i.Evidence = append(i.Evidence, "log-size:"+key+"=unavailable")
		}
	}
	// Program arguments, environment values and log contents never enter the report.
}
