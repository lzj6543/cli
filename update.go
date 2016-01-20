package main

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/heroku/heroku-cli/Godeps/_workspace/src/github.com/dickeyxxx/golock"
	"github.com/heroku/heroku-cli/Godeps/_workspace/src/github.com/franela/goreq"
	"github.com/heroku/heroku-cli/gode"
	"github.com/kardianos/osext"
)

var updateTopic = &Topic{
	Name:        "update",
	Description: "update heroku-cli",
}

var updateCmd = &Command{
	Topic:       "update",
	Hidden:      true,
	Description: "updates heroku-cli",
	Args:        []Arg{{Name: "channel", Optional: true}},
	Flags:       []Flag{{Name: "background", Hidden: true}},
	Run: func(ctx *Context) {
		channel := ctx.Args.(map[string]string)["channel"]
		if channel == "" {
			channel = Channel
		}
		t := "foreground"
		if ctx.Flags["background"] == true {
			t = "background"
		}
		Update(channel, t)
	},
}

var binPath string
var updateLockPath = filepath.Join(AppDir(), "updating.lock")
var autoupdateFile = filepath.Join(AppDir(), "autoupdate")

func init() {
	binPath, _ = osext.Executable()
}

// Update updates the CLI and plugins
func Update(channel string, t string) {
	Debugln("running " + version() + " from " + binPath)
	if !IsUpdateNeeded(t) {
		return
	}
	done := make(chan bool)
	go func() {
		touchAutoupdateFile()
		updateCLI(channel)
		updateNode()
		updatePlugins()
		done <- true
	}()
	select {
	case <-time.After(time.Second * 300):
		Errln("Timed out while updating")
	case <-done:
	}
}

func updatePlugins() {
	plugins := PluginNamesNotSymlinked()
	if len(plugins) == 0 {
		return
	}
	Err("Updating plugins... ")
	packages, err := gode.OutdatedPackages(plugins...)
	PrintError(err, true)
	if len(packages) > 0 {
		for name, version := range packages {
			lockPlugin(name)
			PrintError(gode.InstallPackages(name+"@"+version), true)
			plugin, err := ParsePlugin(name)
			PrintError(err, true)
			AddPluginsToCache(plugin)
			unlockPlugin(name)
		}
		Errf("done. Updated %d %s.\n", len(packages), plural("package", len(packages)))
	} else {
		Errln("no plugins to update.")
	}
}

func updateCLI(channel string) {
	if channel == "?" {
		// do not update dev version
		return
	}
	manifest, err := getUpdateManifest(channel)
	if err != nil {
		Warn("Error updating CLI")
		PrintError(err, false)
		return
	}
	if manifest.Version == Version && manifest.Channel == Channel {
		return
	}
	LogIfError(golock.Lock(updateLockPath))
	unlock := func() {
		golock.Unlock(updateLockPath)
	}
	defer unlock()
	Errf("Updating Heroku v4 CLI to %s (%s)... ", manifest.Version, manifest.Channel)
	build := manifest.Builds[runtime.GOOS][runtime.GOARCH]
	// on windows we can't remove an existing file or remove the running binary
	// so we download the file to binName.new
	// move the running binary to binName.old (deleting any existing file first)
	// rename the downloaded file to binName
	if err := downloadBin(binPath+".new", build.URL); err != nil {
		panic(err)
	}
	if fileSha1(binPath+".new") != build.Sha1 {
		panic("SHA mismatch")
	}
	os.Remove(binPath + ".old")
	os.Rename(binPath, binPath+".old")
	if err := os.Rename(binPath+".new", binPath); err != nil {
		panic(err)
	}
	os.Remove(binPath + ".old")
	Errln("done")
	unlock()
	clearAutoupdateFile() // force full update
	reexec()              // reexec to finish updating with new code
}

// IsUpdateNeeded checks if an update is available
func IsUpdateNeeded(t string) bool {
	f, err := os.Stat(autoupdateFile)
	if err != nil {
		return true
	}
	if t == "background" {
		return time.Since(f.ModTime()) > 4*time.Hour
	} else if t == "block" {
		return time.Since(f.ModTime()) > 2160*time.Hour // 90 days
	}
	return true
}

func touchAutoupdateFile() {
	out, err := os.OpenFile(autoupdateFile, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		panic(err)
	}
	out.WriteString(time.Now().String())
	out.Close()
}

// forces a full update on the next run
func clearAutoupdateFile() {
	PrintError(os.Remove(autoupdateFile), true)
}

type manifest struct {
	Channel, Version string
	Builds           map[string]map[string]struct {
		URL, Sha1 string
	}
}

func getUpdateManifest(channel string) (*manifest, error) {
	res, err := goreq.Request{
		Uri:       "https://cli-assets.heroku.com/" + channel + "/manifest.json",
		ShowDebug: debugging,
	}.Do()
	if err != nil {
		return nil, err
	}
	var m manifest
	res.Body.FromJsonTo(&m)
	return &m, nil
}

func downloadBin(path, url string) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	res, err := goreq.Request{
		Uri:       url + ".gz",
		ShowDebug: debugging,
	}.Do()
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		b, _ := res.Body.ToString()
		return errors.New(b)
	}
	defer res.Body.Close()
	_, err = io.Copy(out, res.Body)
	if err != nil {
		return err
	}
	return out.Close()
}

func fileSha1(path string) string {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha1.Sum(data))
}

// TriggerBackgroundUpdate will trigger an update to the client in the background
func TriggerBackgroundUpdate() {
	if IsUpdateNeeded("background") {
		exec.Command(binPath, "update", "--background").Start()
	}
}

// restarts the CLI with the same arguments
func reexec() {
	Debugln("reexecing new CLI...")
	cmd := exec.Command(binPath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	os.Exit(getExitCode(cmd.Run()))
}
