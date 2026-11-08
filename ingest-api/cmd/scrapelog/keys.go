package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
)

var errUsage = errors.New("usage: scrapelog keys create --project P --name N | list | revoke KEY_ID")

func keys(arguments []string) int {
	loadedConfig, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := runKeysCommand(arguments, "http://127.0.0.1"+listenAddress, loadedConfig.AdminToken, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

func runKeysCommand(arguments []string, baseURL string, adminToken string, output io.Writer) error {
	if len(arguments) == 0 {
		return errUsage
	}
	switch arguments[0] {
	case "create":
		flags := flag.NewFlagSet("keys create", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		project := flags.String("project", "", "")
		name := flags.String("name", "", "")
		if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
			return errUsage
		}
		requestBody, err := json.Marshal(map[string]string{"project": *project, "name": *name})
		if err != nil {
			return err
		}
		return callAdminAPI(http.MethodPost, baseURL+"/v1/admin/keys", adminToken, requestBody, output)
	case "list":
		if len(arguments) != 1 {
			return errUsage
		}
		return callAdminAPI(http.MethodGet, baseURL+"/v1/admin/keys", adminToken, nil, output)
	case "revoke":
		if len(arguments) != 2 {
			return errUsage
		}
		return callAdminAPI(http.MethodDelete, baseURL+"/v1/admin/keys/"+url.PathEscape(arguments[1]), adminToken, nil, output)
	}
	return errUsage
}

func callAdminAPI(method string, requestURL string, adminToken string, requestBody []byte, output io.Writer) error {
	request, err := http.NewRequest(method, requestURL, bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+adminToken)
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("server returned status %d: %s", response.StatusCode, bytes.TrimSpace(responseBody))
	}
	_, err = io.Copy(output, response.Body)
	return err
}
