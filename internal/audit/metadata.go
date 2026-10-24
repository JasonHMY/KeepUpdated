package audit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const metadataLimit = 8 << 20

func (s *Scanner) response(raw string) (*http.Response, string) {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return nil, "invalid_source"
	}
	if s.Config.Context != nil {
		req = req.WithContext(s.Config.Context)
	}
	req.Header.Set("User-Agent", "KeepUpdated/0.1")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, networkCode(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, "not_found"
		}
		return nil, "http_error"
	}
	return resp, ""
}
func (s *Scanner) getBytes(raw string) ([]byte, string) {
	resp, code := s.response(raw)
	if code != "" {
		return nil, code
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, metadataLimit+1))
	if err != nil {
		return nil, networkCode(err)
	}
	if len(b) > metadataLimit {
		return nil, "output_limit"
	}
	return b, ""
}

// PyPI puts project info before the potentially enormous releases history. Consume only that object.
func (s *Scanner) pypiInfo(name string, out any) string {
	resp, code := s.response("https://pypi.org/pypi/" + url.PathEscape(name) + "/json")
	if code != "" {
		return code
	}
	defer resp.Body.Close()
	limited := &io.LimitedReader{R: resp.Body, N: metadataLimit}
	dec := json.NewDecoder(limited)
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return "invalid_response"
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return decodeCode(err, limited.N)
		}
		if key == "info" {
			if err := dec.Decode(out); err != nil {
				return decodeCode(err, limited.N)
			}
			return ""
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return decodeCode(err, limited.N)
		}
	}
	return "missing_project_info"
}
func decodeCode(err error, remaining int64) string {
	if remaining == 0 {
		return "output_limit"
	}
	if networkCode(err) == "timeout" {
		return "timeout"
	}
	return "invalid_response"
}

var brewName = regexp.MustCompile(`^[a-z0-9][a-z0-9@+._-]*$`)
var formulaAlias = regexp.MustCompile(`^\.\./Formula/[a-z0-9]/([a-z0-9@+._-]+)\.rb$`)

// An upstream alias can designate a replacement product. Report it without treating it as an upgrade.
func (s *Scanner) missingBrew(i *Item) {
	if !brewName.MatchString(i.lookup) {
		i.Update = result("unknown", "invalid_package_identity")
		return
	}
	repo, folder := "homebrew-core", "Formula"
	if i.Kind == "cask" {
		repo, folder = "homebrew-cask", "Casks"
	}
	base := "https://raw.githubusercontent.com/Homebrew/" + repo + "/HEAD/"
	target, relation := "", ""
	if i.Kind == "formula" {
		b, code := s.getBytes(base + "Aliases/" + url.PathEscape(i.lookup))
		if code == "" {
			m := formulaAlias.FindStringSubmatch(strings.TrimSpace(string(b)))
			if m == nil {
				i.Update = result("unknown", "unrecognized_upstream_alias")
				return
			}
			target = m[1]
			relation = "alias"
		} else if code != "not_found" {
			i.Update = result("error", code)
			return
		}
	}
	if target == "" {
		file := "formula_renames.json"
		if i.Kind == "cask" {
			file = "cask_renames.json"
		}
		var renames map[string]string
		if code := s.get(base+file, &renames); code != "" {
			i.Update = result("error", code)
			return
		}
		target = renames[i.lookup]
		relation = "rename"
	}
	if target != "" {
		if !brewName.MatchString(target) || target == i.lookup {
			i.Update = result("unknown", "unrecognized_upstream_alias")
			return
		}
		endpoint := "formula"
		if i.Kind == "cask" {
			endpoint = "cask"
		}
		var m struct {
			Name     string `json:"name"`
			Token    string `json:"token"`
			Version  string `json:"version"`
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
		}
		if code := s.get("https://formulae.brew.sh/api/"+endpoint+"/"+url.PathEscape(target)+".json", &m); code != "" {
			i.Update = result("error", code)
			return
		}
		if m.Name != target && m.Token != target {
			i.Update = result("unknown", "identity_mismatch")
			return
		}
		i.Latest = m.Version
		if i.Kind == "formula" {
			i.Latest = m.Versions.Stable
		}
		i.Evidence = append(i.Evidence, "upstream-"+relation+":"+target)
		i.Update = result("unknown", "upstream_"+relation+"_requires_review")
		return
	}
	_, code := s.getBytes(base + path.Join(folder, i.lookup[:1], i.lookup+".rb"))
	if code == "not_found" {
		i.Evidence = append(i.Evidence, "metadata-api:404", "upstream-definition:404")
		i.Update = result("unknown", "upstream_definition_absent")
	} else if code == "" {
		i.Update = result("error", "catalog_definition_mismatch")
	} else {
		i.Update = result("error", code)
	}
}
