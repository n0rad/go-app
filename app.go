package app

import (
	"embed"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/gofrs/flock"
	"github.com/mitchellh/go-homedir"
	"github.com/n0rad/go-app/version"
	"github.com/n0rad/go-erlog/data"
	"github.com/n0rad/go-erlog/errs"
	"github.com/n0rad/go-erlog/logs"
	"gopkg.in/yaml.v3"
)

const pathEmbedded = "embedded"
const pathLock = "lock"
const pathVersion = "version"
const pathConfig = "config.yaml"

type App struct {
	Name         string
	Home         string
	Version      string
	Embedded     *embed.FS
	EmbeddedPath string

	//semVersion version.SemVersion
}

func (app *App) LoadConfig() error {
	configFullPath := filepath.Join(app.Home, pathConfig)
	if stat, err := os.Stat(configFullPath); os.IsNotExist(err) {
		return nil
	} else if stat.IsDir() {
		return errs.WithEF(err, data.WithField("path", configFullPath), "Folder found on config location")
	}

	bytes, err := os.ReadFile(configFullPath)
	if err != nil {
		return errs.WithEF(err, data.WithField("path", configFullPath), "Failed to read config file")
	}

	if err := yaml.Unmarshal(bytes, app); err != nil {
		return errs.WithEF(err, data.WithField("content", string(bytes)).WithField("path", configFullPath), "Failed to parse config file")
	}
	return nil
}

func (app *App) DefaultHomeFolder() string {
	home, err := homedir.Dir()
	if err != nil {
		logs.WithE(err).Warn("Failed to find home directory")
		home = filepath.Join(os.TempDir(), app.Name)
	}
	return filepath.Join(home, ".config/"+app.Name)
}

func (app *App) Init(home string) error {
	// Internal binary app version
	//if semVersion, err := semver.Parse(app.Version); err != nil {
	//	return errs.WithEF(err, data.WithField("Version", app.Version), "Failed to parse application Version")
	//} else {
	//	app.semVersion = version.SemVersion{Version: semVersion}
	//}

	// prepare home
	app.Home = home
	if err := os.MkdirAll(app.Home, 0755); err != nil {
		return errs.WithEF(err, data.WithField("path", app.Home), "Failed to create "+app.Name+" home directory")
	}

	// home version
	lock := flock.New(filepath.Join(app.Home, pathLock))
	if err := lock.Lock(); err != nil {
		return errs.WithE(err, "Failed to get home preparation lock")
	}
	defer lock.Unlock()
	homeVersionBytes, err := os.ReadFile(filepath.Join(app.Home, pathVersion))
	if err != nil {
		logs.WithE(err).Warn("Failed to read home version. May be first run")
	}

	// config
	if err := app.LoadConfig(); err != nil {
		return err
	}

	// embedded
	if app.Embedded != nil {
		app.EmbeddedPath = filepath.Join(app.Home, pathEmbedded, app.Version)
		if app.Version == "0.0.0" || string(homeVersionBytes) != app.Version || err != nil {
			logs.WithField("homeVersion", string(homeVersionBytes)).
				WithField("currentVersion", app.Version).
				Info(app.Name + " version changed")

			if err := os.RemoveAll(app.EmbeddedPath); err != nil {
				logs.WithE(err).Warn("Failed to cleanup current embedded before extract")
			}

			if err := app.extractEmbedded(app.EmbeddedPath); err != nil {
				return errs.WithEF(err, data.WithField("path", app.EmbeddedPath), "Failed to restore embedded")
			}
		}

		if err := app.cleanupEmbedded(); err != nil {
			logs.WithE(err).Warn("Problem during embedded cleanup")
		}
	}

	if string(homeVersionBytes) != app.Version {
		if err := os.WriteFile(filepath.Join(app.Home, pathVersion), []byte(app.Version), 0644); err != nil {
			logs.WithE(err).Error("Failed to write current " + app.Name + " version to home")
		}
	}

	return nil
}

///////////////////

func (app *App) extractEmbedded(target string) error {
	return fs.WalkDir(app.Embedded, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		newPath := filepath.Join(target, path)
		if d.IsDir() {
			return os.MkdirAll(newPath, 0755)
		}

		if !d.Type().IsRegular() {
			return errs.WithF(data.WithField("path", path), "Embedded is invalid, not a regular file")
		}

		r, err := app.Embedded.Open(path)
		if err != nil {
			return err
		}
		defer r.Close()
		info, err := r.Stat()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(newPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644|info.Mode()&0755)
		if err != nil {
			return err
		}

		if _, err := io.Copy(w, r); err != nil {
			w.Close()
			return errs.WithEF(err, data.WithField("path", path), "Failed to extract embedded")
		}
		return w.Close()
	})
}

func (app *App) cleanupEmbedded() error {
	dir, err := os.ReadDir(filepath.Join(app.Home, pathEmbedded))
	if err != nil {
		return errs.WithE(err, "Failed to read home folder")
	}
	var embeddedVersions []string
	for _, entry := range dir {
		embeddedVersions = append(embeddedVersions, entry.Name())
	}

	// Multiple process could be running in parallel and there is no way to know if we can clean up embedded without monitoring process.
	// To not do process monitoring, we can assume the app will not be updated more than 2 times without having process completed
	// So we keep 2 embedded + one being installed
	if len(embeddedVersions) > 3 {
		sort.Slice(embeddedVersions, func(i, j int) bool {
			ai, err := version.Parse(embeddedVersions[i])
			if err != nil {
				logs.WithEF(err, data.WithField("embedded", i)).Warn("Failed to read embedded version")
				return false
			}
			aj, err := version.Parse(embeddedVersions[j])
			if err != nil {
				logs.WithEF(err, data.WithField("embedded", j)).Warn("Failed to read embedded version")
				return false
			}
			return ai.Compare(aj) < 0
		})

		oldestEmbedded := embeddedVersions[0]
		if oldestEmbedded == app.Version {
			logs.WithField("embedded", oldestEmbedded).Debug("oldest app embedded version is currently used version, not cleaning it up")
			return nil
		}
		toCleanupPath := filepath.Join(app.Home, pathEmbedded, oldestEmbedded)
		if err := os.RemoveAll(toCleanupPath); err != nil {
			return errs.WithEF(err, data.WithField("folder", toCleanupPath), "Failed to cleanup old embedded")
		}
	}
	return nil
}
