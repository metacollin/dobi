package tasks

import (
	"fmt"
	"os"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/dnephin/dobi/config"
	"github.com/fsouza/go-dockerclient"
	"github.com/kballard/go-shellquote"
)

// CommandTask is a task which runs a command in a container to produce a
// file or set of files.
type CommandTask struct {
	baseTask
	config *config.CommandConfig
}

// NewCommandTask creates a new CommandTask object
func NewCommandTask(options taskOptions, conf *config.CommandConfig) *CommandTask {
	return &CommandTask{
		baseTask: baseTask{
			name:   options.name,
			client: options.client,
		},
		config: conf,
	}
}

func (t *CommandTask) String() string {
	return fmt.Sprintf("CommandTask(name=%s, config=%s)", t.name, t.config)
}

func (t *CommandTask) logger() *log.Entry {
	return log.WithFields(log.Fields{
		"task":     "Command",
		"name":     t.name,
		"use":      t.config.Use,
		"command":  t.config.Command,
		"artifact": t.config.Artifact,
	})
}

// Run creates the host path if it doesn't already exist
func (t *CommandTask) Run(ctx *ExecuteContext) error {
	t.logger().Info("run")
	stale, err := t.isStale(ctx)
	if !stale || err != nil {
		return err
	}
	t.logger().Debug("artifact is stale")

	err = t.runContainer(ctx)
	if err != nil {
		return err
	}
	ctx.setModified(t.name)
	t.logger().Info("done")
	return nil
}

func (t *CommandTask) isStale(ctx *ExecuteContext) (bool, error) {
	if ctx.isModified(t.config.Dependencies()...) {
		return true, nil
	}

	if t.config.Artifact == "" {
		return true, nil
	}

	artifactLastModified, err := t.artifactLastModified()
	if err != nil {
		return true, err
	}

	volumesLastModified, err := t.volumesLastModified(ctx)
	if err != nil {
		return true, err
	}

	if artifactLastModified.Before(volumesLastModified) {
		t.logger().Debug("artifact older than volume files")
		return true, nil
	}

	image, err := ctx.tasks.images[t.config.Use].getImage(ctx)
	if err != nil {
		return true, err
	}
	if artifactLastModified.Before(image.Created) {
		t.logger().Debug("artifact older than image")
		return true, nil
	}
	return false, nil
}

func (t *CommandTask) artifactLastModified() (time.Time, error) {
	info, err := os.Stat(t.config.Artifact)
	// File or directory doesn't exist
	if err != nil {
		return time.Time{}, nil
	}

	if !info.IsDir() {
		return info.ModTime(), nil
	}

	return lastModified(t.config.Artifact)
}

// TODO: support a .volumeignore file?
func (t *CommandTask) volumesLastModified(ctx *ExecuteContext) (time.Time, error) {
	// TODO: move this iteration to a more appropriate place
	volumePaths := []string{}
	for _, volumeName := range t.config.Volumes {
		// TODO: where does this name get validated that it is a volume?
		volume, _ := ctx.tasks.volumes[volumeName]
		volumePaths = append(volumePaths, volume.config.Path)
	}
	return lastModified(volumePaths...)
}

func (t *CommandTask) volumeBinds(ctx *ExecuteContext) []string {
	binds := []string{}
	for _, volume := range t.config.Volumes {
		volume, _ := ctx.tasks.volumes[volume]
		binds = append(binds, volume.asBind())
	}
	return binds
}

func (t *CommandTask) runContainer(ctx *ExecuteContext) error {
	// TODO: move this to config resource validation?
	command, err := shellquote.Split(t.config.Command)
	if err != nil {
		return err
	}

	// TODO: set a unique container name
	// TODO: support other run options
	container, err := t.client.CreateContainer(docker.CreateContainerOptions{
		// TODO: give the container a unique name based on UNIQUE_ID and step
		// name
		Config: &docker.Config{
			Cmd:       command,
			Image:     ctx.tasks.images[t.config.Use].getImageName(ctx),
			OpenStdin: t.config.Interactive,
			Tty:       t.config.Interactive,
		},
		HostConfig: &docker.HostConfig{
			// TODO: support relative paths or {{PWD}}
			Binds:      t.volumeBinds(ctx),
			Privileged: t.config.Privileged,
		},
	})
	if err != nil {
		return err
	}

	if err := t.client.StartContainer(container.ID, nil); err != nil {
		return err
	}

	if err := t.client.AttachToContainer(docker.AttachToContainerOptions{
		Container: container.ID,
		// TODO: send this to a buffer for --quiet
		OutputStream: os.Stdout,
		ErrorStream:  os.Stderr,
		InputStream:  os.Stdin,
		Logs:         false,
		Stream:       true,
		Stdin:        t.config.Interactive,
		RawTerminal:  t.config.Interactive,
		Stdout:       true,
		Stderr:       true,
	}); err != nil {
		return err
	}

	// TODO: stop container first if interactive?
	if err := t.client.RemoveContainer(docker.RemoveContainerOptions{
		ID:            container.ID,
		RemoveVolumes: true,
	}); err != nil {
		t.logger().WithFields(log.Fields{
			"container": container.ID,
			"error":     err.Error(),
		}).Warn("Failed to remove container")
	}
	return nil
}
