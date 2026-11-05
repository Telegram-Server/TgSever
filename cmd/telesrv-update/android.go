package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"telesrv/internal/updatecdn"
)

// androidFlags 是发布 Android 安装包时运营者填写的部分；版本、包名与签名证书从 APK 读出。
type androidFlags struct {
	channel    *string
	notesEN    *string
	notesZH    *string
	notesRU    *string
	notesFile  *string
	canNotSkip *bool
	url        *string
}

func registerAndroidFlags(fs *flag.FlagSet) androidFlags {
	return androidFlags{
		channel:    fs.String("channel", "", "Android APK: channel to publish to (stable, beta or alpha)"),
		notesEN:    fs.String("notes-en", "", "Android APK: release notes in English"),
		notesZH:    fs.String("notes-zh", "", "Android APK: release notes in Chinese"),
		notesRU:    fs.String("notes-ru", "", "Android APK: release notes in Russian"),
		notesFile:  fs.String("notes-file", "", `Android APK: JSON file of release notes by language, e.g. {"en":"...","zh":"..."}; merged under the -notes-* flags`),
		canNotSkip: fs.Bool("can-not-skip", false, "Android APK: forced update (clients cannot dismiss it)"),
		url:        fs.String("url", "", "Android APK: optional fallback download URL, only used by the forced-update screen"),
	}
}

// set 报告是否给了任何 Android 专用参数（发布桌面包时不允许）。
func (f androidFlags) set() bool {
	return *f.channel != "" || *f.notesEN != "" || *f.notesZH != "" || *f.notesRU != "" || *f.notesFile != "" || *f.canNotSkip || *f.url != ""
}

func (f androidFlags) notes() (map[string]string, error) {
	notes := map[string]string{}
	if path := strings.TrimSpace(*f.notesFile); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read -notes-file: %w", err)
		}
		if err := decodeNotes(data, notes); err != nil {
			return nil, fmt.Errorf("-notes-file %s: %w", path, err)
		}
	}
	for lang, text := range map[string]string{"en": *f.notesEN, "zh": *f.notesZH, "ru": *f.notesRU} {
		if strings.TrimSpace(text) != "" {
			notes[lang] = text
		}
	}
	return notes, nil
}

func publishAndroid(paths catalogPaths, apkPath string, flags androidFlags, dryRun, allowDowngrade bool) error {
	notes, err := flags.notes()
	if err != nil {
		return err
	}
	var opts updatecdn.PublishOptions
	if paths.RootPublicKey != "" {
		// 新清单按服务加载时的规则完整校验；有启用的桌面条目时需要根公钥。
		rootKey, err := updatecdn.LoadRootPublicKey(paths.RootPublicKey)
		if err != nil {
			return err
		}
		opts.RootPublicKey = rootKey
	}
	opts.DryRun, opts.AllowDowngrade = dryRun, allowDowngrade
	result, err := updatecdn.PublishAndroidPackage(paths.Manifest, paths.FilesDir, apkPath, updatecdn.AndroidReleaseMeta{
		Channel:    *flags.channel,
		Notes:      notes,
		CanNotSkip: *flags.canNotSkip,
		URL:        *flags.url,
	}, opts)
	if err != nil {
		return fmt.Errorf("publish %s: %w", apkPath, err)
	}
	var status string
	switch {
	case result.Unchanged:
		status = "already published"
	case !result.ManifestUpdated && dryRun:
		status = "dry run: would restore the missing package file of"
	case !result.ManifestUpdated:
		status = "restored the missing package file of"
	case dryRun:
		status = "dry run: would publish"
	default:
		status = "published"
	}
	release := result.Release
	fmt.Printf("%s apps.android.%s\n", status, result.Channel)
	fmt.Printf("  package: %s\n", release.PackageName)
	fmt.Printf("  version: %s (versionCode %d, minSdk %d)\n", release.Version, release.VersionCode, result.APK.MinSDK)
	fmt.Printf("  signer:  %s (APK signature scheme v%d)\n", release.CertSHA256, result.APK.SigningScheme)
	fmt.Printf("  file:    %s\n", release.File)
	fmt.Printf("  size:    %d\n", release.Size)
	fmt.Printf("  sha256:  %s\n", release.SHA256)
	langs := make([]string, 0, len(release.Notes))
	for lang := range release.Notes {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	fmt.Printf("  notes:   %s\n", strings.Join(langs, ", "))
	if release.CanNotSkip {
		fmt.Println("  forced:  yes (clients cannot dismiss it)")
	}
	if previous := result.Previous; previous != nil && result.ManifestUpdated {
		state := ""
		if previous.Disabled {
			state = ", disabled"
		}
		fmt.Printf("  replaces: %s (versionCode %d, %s%s)\n", previous.Version, previous.VersionCode, previous.File, state)
	}
	fmt.Printf("  manifest: %s\n", paths.Manifest)
	if !dryRun && result.ManifestUpdated {
		fmt.Println("telesrv registers the APK as a Telegram document in the background; Android clients see the update once that is done.")
	}
	return nil
}
