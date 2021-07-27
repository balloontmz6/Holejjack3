// Copyright (c) Huawei Technologies Co., Ltd. 2019-2019. All rights reserved.
// iSulad-kit licensed under the Mulan PSL v1.
// You can use this software according to the terms and conditions of the Mulan PSL v1.
// You may obtain a copy of Mulan PSL v1 at:
//     http://license.coscl.org.cn/MulanPSL
// THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND, EITHER EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT, MERCHANTABILITY OR FIT FOR A PARTICULAR
// PURPOSE.
// See the Mulan PSL v1 for more details.
// Description: iSulad image kit
// Author: lifeng
// Create: 2019-05-06

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	istorage "github.com/containers/image/storage"
	"github.com/containers/image/types"
	cstorage "github.com/containers/storage"
	"github.com/docker/docker/pkg/homedir"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/urfave/cli"
)

// AuthInfo provide basic information about auth.
type AuthInfo struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Auth     string `json:"auth,omitempty"`
}

const maxJSONFileSize = (10 * 1024 * 1024)

func defaultAuthFilePath() string {
	return filepath.Join(homedir.Get(), ".isulad/auths.json")
}

func useDecryptedKey(c *cli.Context, flagPrefix string) types.OptionalBool {
	if c.IsSet(flagPrefix + "use-decrypted-key") {
		return types.NewOptionalBool(c.BoolT(flagPrefix + "use-decrypted-key"))
	}

	// If not set, default true.
	return types.NewOptionalBool(true)
}

func tlsVerify(c *cli.Context, flagPrefix string) bool {
	if c.IsSet(flagPrefix + "tls-verify") {
		return c.BoolT(flagPrefix + "tls-verify")
	}

	// If not set, default true.
	return true
}

func contextFromGlobalOptions(c *cli.Context, flagPrefix string) (*types.SystemContext, error) {
	ctx := &types.SystemContext{
		RegistriesDirPath:                 c.GlobalString("registries.d"),
		ArchitectureChoice:                c.GlobalString("override-arch"),
		OSChoice:                          c.GlobalString("override-os"),
		DockerCertPath:                    c.String(flagPrefix + "cert-dir"),
		DockerInsecureSkipTLSVerify:       types.NewOptionalBool(!tlsVerify(c, flagPrefix)),
		OSTreeTmpDirPath:                  c.String(flagPrefix + "ostree-tmp-dir"),
		OCISharedBlobDirPath:              c.String(flagPrefix + "shared-blob-dir"),
		DirForceCompress:                  c.Bool(flagPrefix + "compress"),
		AuthFilePath:                      c.String("authfile"),
		DockerDaemonHost:                  c.String(flagPrefix + "daemon-host"),
		DockerDaemonCertPath:              c.String(flagPrefix + "cert-dir"),
		DockerDaemonInsecureSkipTLSVerify: !c.BoolT(flagPrefix + "tls-verify"),
		UseDecryptedKey:                   useDecryptedKey(c, flagPrefix),
	}
	if c.IsSet(flagPrefix + "creds") {
		var err error
		ctx.DockerAuthConfig, err = getDockerAuth(c.String(flagPrefix + "creds"))
		if err != nil {
			return nil, err
		}
	}
	if c.IsSet(flagPrefix + "authfile") {
		ctx.AuthFilePath = c.String(flagPrefix + "authfile")
	}
	if ctx.AuthFilePath == "" {
		ctx.AuthFilePath = defaultAuthFilePath()
	}
	return ctx, nil
}

func commandTimeoutContextFromGlobalOptions(c *cli.Context) (context.Context, context.CancelFunc) {
	ctx := context.Background()
	var cancel context.CancelFunc = func() {}
	if c.GlobalDuration("command-timeout") > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.GlobalDuration("command-timeout"))
	}
	return ctx, cancel
}

func parseCreds(creds string) (string, string, error) {
	if creds == "" {
		return "", "", errors.New("credentials can't be empty")
	}
	up := strings.SplitN(creds, ":", 2)
	if len(up) == 1 {
		return up[0], "", nil
	}
	if up[0] == "" {
		return "", "", errors.New("username can't be empty")
	}
	return up[0], up[1], nil
}

func getDockerAuth(creds string) (*types.DockerAuthConfig, error) {
	username, password, err := parseCreds(creds)
	if err != nil {
		return nil, err
	}
	return &types.DockerAuthConfig{
		Username: username,
		Password: password,
	}, nil
}

func getMountPoint(c *cli.Context, idOrName string) (string, error) {
	_, cancel := commandTimeoutContextFromGlobalOptions(c)
	defer cancel()

	store, err := getEmptyStorageStore(c)
	if err != nil {
		return "", err
	}

	mountPoint, err := store.Mount(idOrName, "")
	if err != nil {
		return "", fmt.Errorf("Failed to mount container %s: %v", idOrName, err)
	}

	return mountPoint, nil
}

func checkJSONFileSize(path string) error {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	fileSize := fileInfo.Size()
	if fileSize > maxJSONFileSize {
		return fmt.Errorf("%s is too large", filepath.Base(path))
	}
	return nil
}

func readAuthFromStdin() (string, string, error) {
	var (
		username string
		password string
		authData AuthInfo
	)

	inputReader := bufio.NewReader(os.Stdin)
	line, _, err := inputReader.ReadLine()
	if err != nil {
		return "", "", fmt.Errorf("error reading authentication: %v", err)
	}

	if err := json.Unmarshal(line, &authData); err != nil {
		return "", "", fmt.Errorf("error unmarshal authentication: %v", err)
	}

	if authData.Username != "" {
		username = authData.Username
	}

	if authData.Password != "" {
		password = authData.Password
	}

	if authData.Auth != "" {
		username, password, err = decodeAuth(authData.Auth)
		if err != nil {
			return "", "", fmt.Errorf("error decoding authentication: %v", err)
		}
	}
	return username, password, nil
}

func getImageCloser(store cstorage.Store, containerImageName string) (types.ImageCloser, error) {
	// Check if we have the specified image.
	ref, err := istorage.Transport.ParseStoreReference(store, containerImageName)
	if err != nil {
		return nil, err
	}
	// Pull out a copy of the image's configuration.
	image, err := ref.NewImage(context.Background(), &types.SystemContext{})
	if err != nil {
		return nil, err
	}

	return image, nil
}

func getImageConf(store cstorage.Store, containerImageName string) (*v1.Image, error) {
	tmpImage, err := getImageCloser(store, containerImageName)
	if err != nil {
		return nil, err
	}
	defer tmpImage.Close()

	return tmpImage.OCIConfig(context.Background())
}

func getHealthcheck(store cstorage.Store, containerImageName string) (*HealthConfig, error) {
	tmpImage, err := getImageCloser(store, containerImageName)
	if err != nil {
		return nil, err
	}
	defer tmpImage.Close()

	cb, err := tmpImage.ConfigBlob(context.Background())
	if err != nil {
		return nil, err
	}
	config := &ConfigFromJSON{}
	if err := json.Unmarshal(cb, config); err != nil {
		return nil, err
	}
	return config.Config.Healthcheck, nil
}