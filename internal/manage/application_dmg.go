package manage

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// retained prevents the caller from removing a container that may still hold a mount.
func stageApplicationArchive(ctx context.Context, runner Runner, data []byte, stage string, p ApplicationUpdatePreview) (app string, retained bool, err error) {
	switch p.Archive.Format {
	case "":
		app, err = extractApplicationZIP(ctx, data, stage)
		if err != nil && err.Error() == "application_zip_invalid" {
			err = errors.New("application_archive_format_changed")
		}
		return
	case "dmg":
		// Only UDIF images are supported; never send a mislabeled archive to the mounter.
		if len(data) < 512 || string(data[len(data)-512:len(data)-508]) != "koly" {
			return "", false, errors.New("application_archive_format_changed")
		}
	default:
		return "", false, errors.New("application_archive_format_changed")
	}
	container := filepath.Dir(stage)
	image := filepath.Join(container, "payload.dmg")
	mount := filepath.Join(container, "mount")
	if err = os.WriteFile(image, data, 0600); err != nil {
		return
	}
	if err = os.Mkdir(mount, 0700); err != nil {
		return
	}
	// A canceled or failed attach may have mounted before losing its reply. Retain
	// the evidence until a known device has been detached; never guess a device.
	retained = true
	device := ""
	reason := "application_dmg_mount_unverified"
	record := func() error {
		operationReason := ""
		if err != nil {
			operationReason = applicationUpdateError(err)
		}
		if ctx.Err() != nil {
			operationReason = applicationUpdateError(ctx.Err())
		}
		b, _ := json.MarshalIndent(map[string]string{"reason": reason, "operation_reason": operationReason, "device": device, "mountpoint": mount, "image": image}, "", "  ")
		return os.WriteFile(filepath.Join(container, "DMG-CLEANUP.json"), b, 0600)
	}
	if err = record(); err != nil {
		retained = false
		return
	}
	defer func() {
		if device == "" {
			record()
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if _, e := appCommand(cleanup, runner, "/usr/bin/hdiutil", "detach", device); e != nil {
			reason = "application_dmg_detach_failed"
			record()
			err = errors.New(reason)
			app = ""
			return
		}
		retained = false
		_ = os.Remove(filepath.Join(container, "DMG-CLEANUP.json"))
	}()
	b, attachErr := appCommand(ctx, runner, "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-noautofsck", "-mount", "required", "-mountpoint", mount, "-plist", image)
	result := filepath.Join(container, "attach.plist")
	if err = os.WriteFile(result, b, 0600); err != nil {
		return
	}
	// Parse the native plist before trusting any path or device from it.
	parseCtx, parseCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer parseCancel()
	parsed, parseErr := appCommand(parseCtx, runner, "/usr/bin/plutil", "-extract", "system-entities", "json", "-o", "-", result)
	var entities []struct {
		Device    string `json:"dev-entry"`
		Mount     string `json:"mount-point"`
		Potential bool   `json:"potentially-mountable"`
		Content   string `json:"content-hint"`
	}
	if parseErr != nil || json.Unmarshal(parsed, &entities) != nil || len(entities) == 0 {
		err = errors.New(reason)
		return
	}
	disk := regexp.MustCompile(`^/dev/disk[0-9]+$`)
	partition := regexp.MustCompile(`^/dev/disk[0-9]+(?:s[0-9]+)*$`)
	// hdiutil may list a partition before its whole disk. APFS also exposes a
	// synthesized container disk; detach the unique physical image device.
	for _, entity := range entities {
		if entity.Content == "GUID_partition_scheme" || entity.Content == "Apple_partition_scheme" || len(entities) == 1 && entity.Mount == mount {
			if !disk.MatchString(entity.Device) || device != "" {
				device = ""
				err = errors.New(reason)
				return
			}
			device = entity.Device
		}
	}
	if device == "" {
		err = errors.New(reason)
		return
	}
	if err = record(); err != nil {
		return
	}
	synthesized := ""
	for _, entity := range entities {
		if disk.MatchString(entity.Device) && entity.Device != device {
			if entity.Content != "EF57347C-0000-11AA-AA11-00306543ECAC" || synthesized != "" {
				err = errors.New(reason)
				return
			}
			synthesized = entity.Device
		}
	}
	mounts := 0
	devices := map[string]bool{}
	for _, entity := range entities {
		switch entity.Content {
		case "GUID_partition_scheme", "Apple_partition_scheme", "Apple_partition_map", "Apple_Free", "Apple_APFS", "7C3457EF-0000-11AA-AA11-00306543ECAC", "EF57347C-0000-11AA-AA11-00306543ECAC":
		case "Apple_HFS", "Apple_HFSX", "48465300-0000-11AA-AA11-00306543ECAC", "41504653-0000-11AA-AA11-00306543ECAC":
			if entity.Mount == "" {
				err = errors.New("application_dmg_multiple_volumes")
				return
			}
		default:
			err = errors.New(reason)
			return
		}
		knownDevice := entity.Device == device || strings.HasPrefix(entity.Device, device+"s") || synthesized != "" && (entity.Device == synthesized || strings.HasPrefix(entity.Device, synthesized+"s"))
		if !knownDevice || devices[entity.Device] || !partition.MatchString(entity.Device) || entity.Mount != "" && entity.Mount != mount || entity.Potential && entity.Mount == "" {
			err = errors.New("application_dmg_multiple_volumes")
			return
		}
		devices[entity.Device] = true
		if entity.Mount != "" {
			mounts++
		}
	}
	if attachErr != nil || mounts != 1 {
		err = errors.New(reason)
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
		return
	}
	root, e := applicationDMGRoot(mount)
	if e != nil {
		err = e
		return
	}
	app = filepath.Join(stage, filepath.Base(root))
	if err = copyApplicationDMG(ctx, runner, root, app); err != nil {
		return
	}
	// Validate paths before native verifiers read any bundle metadata or links.
	if err = verifyApplicationPayload(ctx, runner, root, p); err != nil {
		return
	}
	if err = verifyApplicationPayload(ctx, runner, app, p); err != nil {
		return
	}
	original, e := applicationTreeHash(ctx, root)
	if e != nil {
		err = e
		return
	}
	copied, e := applicationTreeHash(ctx, app)
	if e != nil || original != copied {
		err = errors.New("application_dmg_copy_mismatch")
		return
	}
	return
}

func applicationDMGRoot(mount string) (string, error) {
	entries, err := os.ReadDir(mount)
	if err != nil {
		return "", errors.New("application_dmg_payload_unavailable")
	}
	root := ""
	for _, entry := range entries {
		path := filepath.Join(mount, entry.Name())
		st, e := os.Lstat(path)
		if e != nil {
			return "", e
		}
		switch {
		case strings.HasSuffix(strings.ToLower(entry.Name()), ".app") && st.IsDir():
			if root != "" {
				return "", errors.New("application_dmg_single_bundle_required")
			}
			root = path
		case entry.Name() == "Applications" && st.Mode()&os.ModeSymlink != 0:
			target, e := os.Readlink(path)
			if e != nil || target != "/Applications" {
				return "", errors.New("application_dmg_unknown_payload")
			}
		case (entry.Name() == ".DS_Store" || entry.Name() == ".VolumeIcon.icns") && st.Mode().IsRegular():
		case entry.Name() == ".background" && st.IsDir():
			// Only ordinary background images are decoration, not arbitrary payloads.
			images, e := os.ReadDir(path)
			if e != nil {
				return "", errors.New("application_dmg_unknown_payload")
			}
			for _, image := range images {
				info, e := os.Lstat(filepath.Join(path, image.Name()))
				ext := strings.ToLower(filepath.Ext(image.Name()))
				if e != nil || !info.Mode().IsRegular() || (ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".tif" && ext != ".tiff" && ext != ".icns") {
					return "", errors.New("application_dmg_unknown_payload")
				}
			}
		default:
			return "", errors.New("application_dmg_unknown_payload")
		}
	}
	if root == "" {
		return "", errors.New("application_dmg_single_bundle_required")
	}
	return root, nil
}

func copyApplicationDMG(ctx context.Context, runner Runner, root, dest string) error {
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("application_dmg_single_bundle_required")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("application_dmg_payload_unavailable")
	}
	root = realRoot
	var total int64
	count := 0
	seen := map[string]bool{}
	var directories []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		key := strings.ToLower(rel)
		count++
		if count > 100000 {
			return errors.New("application_dmg_limit")
		}
		if seen[key] {
			return errors.New("application_dmg_duplicate_path")
		}
		seen[key] = true
		if strings.HasSuffix(key, ".pkg") || strings.HasSuffix(key, ".mpkg") {
			return errors.New("application_dmg_package_unsupported")
		}
		if strings.ContainsAny(rel, "\\\x00:") {
			return errors.New("application_dmg_unsafe_path")
		}
		target := filepath.Join(dest, rel)
		// Filesystem aliases may be broader than Unicode lowercasing. Never let
		// cp overwrite a previous entry or follow an existing destination link.
		if _, e := os.Lstat(target); !os.IsNotExist(e) {
			if e != nil {
				return e
			}
			return errors.New("application_dmg_duplicate_path")
		}
		switch {
		case st.IsDir():
			if e := os.Mkdir(target, 0700); e != nil {
				return e
			}
			directories = append(directories, path)
		case st.Mode()&os.ModeSymlink != 0:
			link, e := os.Readlink(path)
			if e != nil || filepath.IsAbs(link) || strings.ContainsAny(link, "\\\x00:") || !under(filepath.Clean(filepath.Join(filepath.Dir(path), link)), root) {
				return errors.New("application_dmg_escaping_symlink")
			}
			final, e := filepath.EvalSymlinks(path)
			if e != nil || !under(final, root) {
				return errors.New("application_dmg_escaping_symlink")
			}
			// cp -P preserves the link and its metadata without traversing it.
			if _, e = appCommand(ctx, runner, "/bin/cp", "-pPn", path, target); e != nil {
				return e
			}
		case st.Mode().IsRegular():
			size, e := applicationDMGFileSize(path, st)
			if e != nil {
				return e
			}
			if total+size > 4<<30 {
				return errors.New("application_dmg_limit")
			}
			// One Lstat-checked file at a time, from a read-only image. Native cp preserves
			// resource forks/extended attributes required by bundle signatures.
			if _, e = appCommand(ctx, runner, "/bin/cp", "-pPn", path, target); e != nil {
				return e
			}
			actual, e := os.Lstat(target)
			if e != nil || !actual.Mode().IsRegular() || actual.Size() != st.Size() {
				return errors.New("application_dmg_copy_mismatch")
			}
			copied, e := applicationDMGFileSize(target, actual)
			if e != nil {
				return e
			}
			if copied != size {
				return errors.New("application_dmg_copy_mismatch")
			}
			total += copied
			if total > 4<<30 {
				return errors.New("application_dmg_limit")
			}
		default:
			return errors.New("application_dmg_special_file")
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Restore directory modes after copying so read-only bundle directories work.
	for n := len(directories) - 1; n >= 0; n-- {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path := directories[n]
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, path)
		if e = os.Chmod(filepath.Join(dest, rel), st.Mode().Perm()); e != nil {
			return errors.New("application_dmg_copy_mismatch")
		}
	}
	return nil
}

// Resource forks are copied by cp -p as well; the data fork's stat size alone
// must not allow a file to exceed the copy limits through its resource fork.
func applicationDMGFileSize(path string, info fs.FileInfo) (int64, error) {
	size := info.Size()
	if size < 0 || size > 2<<30 {
		return 0, errors.New("application_dmg_limit")
	}
	fork, err := os.Stat(filepath.Join(path, "..namedfork", "rsrc"))
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return size, nil
		}
		return 0, err
	}
	if !fork.Mode().IsRegular() || fork.Size() < 0 || fork.Size() > (2<<30)-size {
		return 0, errors.New("application_dmg_limit")
	}
	return size + fork.Size(), nil
}
