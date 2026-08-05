package machinedaemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

func newSupervisorToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (c *Client) machineStore() (localstore.MachineStore, error) {
	store, err := localstore.New(c.cfg.OmnaraHome)
	if err != nil {
		return localstore.MachineStore{}, err
	}
	binding, err := c.localMachineBinding()
	if err != nil {
		return localstore.MachineStore{}, err
	}
	return store.Machine(binding.InstallationID, binding.MachineID)
}

func (c *Client) localMachineBinding() (daemonBootstrap, error) {
	if c.bootstrap.InstallationID != "" && c.bootstrap.MachineID != "" {
		return c.bootstrap, nil
	}
	if c.cfg.ExpectedInstallationID != "" &&
		c.cfg.ExpectedMachineID != "" {
		return daemonBootstrap{
			InstallationID: c.cfg.ExpectedInstallationID,
			MachineID:      c.cfg.ExpectedMachineID,
		}, nil
	}
	return daemonBootstrap{}, errors.New(
		"local machine binding is unavailable",
	)
}
