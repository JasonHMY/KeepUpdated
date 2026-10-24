package manage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
)

// uv 0.13.1 persists the invocation's --no-build in its receipt. Preserve the
// user's options only when that is the sole semantic change; never undo another
// native receipt change, interpreter change, or concurrent edit.
func captureUVReceipt(p PlanItem, state, planID string) ([]byte, string, error) {
	if p.Binding.Python == nil {
		return nil, "", errors.New("missing Python policy")
	}
	data, err := readMetadata(filepath.Join(p.Binding.Scope, "uv-receipt.toml"))
	if err != nil {
		return nil, "", err
	}
	doc, err := parseUVReceipt(data)
	if err != nil {
		return nil, "", err
	}
	canonical, _ := json.Marshal(doc)
	if hashText(string(canonical)) != p.Binding.Python.IntentFingerprint {
		return nil, "", errors.New("receipt changed")
	}
	backup := filepath.Join(state, "uv-receipt-"+planID+"-"+hashText(p.Binding.Scope)+".toml")
	if err = writeNativeBackup(backup, data); err != nil {
		return nil, "", err
	}
	return data, backup, nil
}

func restoreUVInvocationOption(p PlanItem, original []byte) error {
	if p.Binding.Python == nil {
		return errors.New("missing Python policy")
	}
	realScope, scopeErr := filepath.EvalSymlinks(p.Binding.Scope)
	if scopeErr != nil || realScope != p.Binding.Python.EnvironmentRealPath {
		return errors.New("environment changed")
	}
	path := filepath.Join(p.Binding.Scope, "uv-receipt.toml")
	actual, err := readMetadata(path)
	if err != nil {
		return err
	}
	before, err := parseUVReceipt(original)
	if err != nil {
		return err
	}
	after, err := parseUVReceipt(actual)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(before, after) {
		return nil
	}
	options, ok := after["tool.options"].(map[string]any)
	if !ok || options["no-build"] != true {
		return errors.New("unexpected native receipt change")
	}
	originalOptions, _ := before["tool.options"].(map[string]any)
	if v, exists := originalOptions["no-build"]; exists {
		options["no-build"] = v
	} else {
		delete(options, "no-build")
	}
	if len(options) == 0 && before["tool.options"] == nil {
		delete(after, "tool.options")
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("unexpected native receipt change")
	}
	interpreter, err := pythonInterpreterHash(p.Binding.Scope)
	if err != nil || interpreter != p.Binding.Python.InterpreterFingerprint {
		return errors.New("interpreter changed")
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || ownedPath(path, false) != nil {
		return errors.New("receipt path changed")
	}
	tmp, err := os.CreateTemp(p.Binding.Scope, ".keepupdated-receipt-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(original); err == nil {
		err = tmp.Chmod(st.Mode().Perm())
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	current, err := readMetadata(path)
	now, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !os.SameFile(st, now) || !bytes.Equal(current, actual) {
		return errors.New("receipt changed before restore")
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	restored, err := readMetadata(path)
	if err != nil || !bytes.Equal(restored, original) {
		return errors.New("receipt restore unverified")
	}
	return nil
}
