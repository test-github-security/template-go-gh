// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package proxy provides utilities for accessing the Go module proxy.
package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/vulndb/internal/version"
)

var DefaultClient *Client

// Client is a client for reading from the proxy.
//
// It uses a simple in-memory cache that does not expire,
// which is acceptable because we use this Client in a short-lived
// context (~1 day at most, in the case of the worker, and a few seconds
// in the case of the vulnreport command), and module/version data does
// not change often enough to be a problem for our use cases.
type Client struct {
	*http.Client
	url    string
	cache  *cache
	errLog *errLog // for testing
}

func init() {
	proxyURL := "https://proxy.golang.org"
	if proxy, ok := os.LookupEnv("GOPROXY"); ok {
		proxyURL = proxy
	}
	DefaultClient = NewClient(http.DefaultClient, proxyURL)
}

func NewClient(c *http.Client, url string) *Client {
	return &Client{
		Client: c,
		url:    url,
		cache:  newCache(),
		errLog: newErrLog(),
	}
}

// Response is a representation of an HTTP response used to
// facilitate testing.
type Response struct {
	Body       string `json:"body,omitempty"`
	StatusCode int    `json:"status_code"`
}

// NewTestClient creates a client that returns hard-coded mock responses.
// endpointsToResponses is a map from proxy endpoints
// (with no server url, and no leading '/'), to their desired responses.
func NewTestClient(endpointsToResponses map[string]*Response) (c *Client, cleanup func()) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		for endpoint, response := range endpointsToResponses {
			if r.Method == http.MethodGet &&
				r.URL.Path == "/"+endpoint {
				if response.StatusCode == http.StatusOK {
					_, _ = w.Write([]byte(response.Body))
				} else {
					w.WriteHeader(response.StatusCode)
				}
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest)
	}
	s := httptest.NewServer(http.HandlerFunc(handler))
	return NewClient(s.Client(), s.URL), func() { s.Close() }
}

func (c *Client) lookup(urlSuffix string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s", c.url, urlSuffix)
	if b, found := c.cache.get(urlSuffix); found {
		return b, nil
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		c.errLog.set(urlSuffix, resp.StatusCode)
		return nil, fmt.Errorf("HTTP GET /%s returned status %v", urlSuffix, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	c.cache.set(urlSuffix, b)
	return b, nil
}

func CanonicalModulePath(path, version string) (string, error) {
	return DefaultClient.CanonicalModulePath(path, version)
}

func (c *Client) CanonicalModulePath(path, version string) (_ string, err error) {
	escapedPath, err := module.EscapePath(path)
	if err != nil {
		return "", err
	}
	escapedVersion, err := module.EscapeVersion(version)
	if err != nil {
		return "", err
	}
	b, err := c.lookup(fmt.Sprintf("%s/@v/%s.mod", escapedPath, escapedVersion))
	if err != nil {
		return "", err
	}
	m, err := modfile.ParseLax("go.mod", b, nil)
	if err != nil {
		return "", err
	}
	if m.Module == nil {
		return "", fmt.Errorf("unable to retrieve module information for %s, %s", path, string(b))
	}
	return m.Module.Mod.Path, nil
}

func CanonicalModuleVersion(path, ver string) (_ string, err error) {
	return DefaultClient.CanonicalModuleVersion(path, ver)
}

// CanonicalModuleVersion returns the canonical version string (with no leading "v" prefix)
// for the given module path and version string.
func (c *Client) CanonicalModuleVersion(path, ver string) (_ string, err error) {
	escaped, err := module.EscapePath(path)
	if err != nil {
		return "", err
	}
	b, err := c.lookup(fmt.Sprintf("%s/@v/%v.info", escaped, ver))
	if err != nil {
		return "", err
	}
	var val map[string]any
	if err := json.Unmarshal(b, &val); err != nil {
		return "", err
	}
	v, ok := val["Version"].(string)
	if !ok {
		return "", fmt.Errorf("unable to retrieve canonical version for %s", ver)
	}
	return version.TrimPrefix(v), nil
}

func Latest(path string) (string, error) {
	return DefaultClient.Latest(path)
}

// Latest returns the latest version of the module, with no leading "v"
// prefix.
func (c *Client) Latest(path string) (string, error) {
	escaped, err := module.EscapePath(path)
	if err != nil {
		return "", err
	}
	b, err := c.lookup(fmt.Sprintf("%s/@latest", escaped))
	if err != nil {
		return "", err
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	ver, ok := v["Version"].(string)
	if !ok {
		return "", fmt.Errorf("unable to retrieve latest version for %s", path)
	}
	return version.TrimPrefix(ver), nil
}

func Versions(path string) ([]string, error) {
	return DefaultClient.Versions(path)
}

// Versions returns a list of module versions (with no leading "v" prefix),
// sorted in ascending order.
func (c *Client) Versions(path string) ([]string, error) {
	escaped, err := module.EscapePath(path)
	if err != nil {
		return nil, err
	}
	b, err := c.lookup(fmt.Sprintf("%s/@v/list", escaped))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}
	var vs []string
	for _, v := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		vs = append(vs, version.TrimPrefix(v))
	}
	sort.SliceStable(vs, func(i, j int) bool {
		return version.Before(vs[i], vs[j])
	})
	return vs, nil
}

func FindModule(path string) string {
	return DefaultClient.FindModule(path)
}

// FindModule returns the longest directory prefix of path that
// is a module, or "" if no such prefix is found.
func (c *Client) FindModule(modPath string) string {
	for candidate := modPath; candidate != "."; candidate = path.Dir(candidate) {
		escaped, err := module.EscapePath(candidate)
		if err != nil {
			return modPath
		}
		if _, err := c.lookup(fmt.Sprintf("%s/@v/list", escaped)); err != nil {
			// Keep looking.
			continue
		}
		return candidate
	}
	return ""
}

func Responses() map[string]*Response {
	return DefaultClient.Responses()
}

// Responses returns a map from endpoints to the latest response received for each endpoint.
//
// Intended for testing: the output can be passed to NewTestClient to create a mock client
// that returns the same responses.
func (c *Client) Responses() map[string]*Response {
	m := make(map[string]*Response)
	for key, status := range c.errLog.getData() {
		m[key] = &Response{StatusCode: status}
	}
	for key, b := range c.cache.getData() {
		m[key] = &Response{Body: string(b), StatusCode: http.StatusOK}
	}
	return m
}

// A simple in-memory cache that never expires.
type cache struct {
	data map[string][]byte
	hits int // for testing
	mu   sync.Mutex
}

func newCache() *cache {
	return &cache{data: make(map[string][]byte)}
}

func (c *cache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if b, ok := c.data[key]; ok {
		c.hits++
		return b, true
	}

	return nil, false
}

func (c *cache) set(key string, val []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data[key] = val
}

func (c *cache) getData() map[string][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.data
}

// An in-memory store of the errors seen so far.
// Used by the Responses() function, for testing.
type errLog struct {
	data map[string]int
	mu   sync.Mutex
}

func newErrLog() *errLog {
	return &errLog{data: make(map[string]int)}
}

func (e *errLog) set(key string, status int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.data[key] = status
}

func (e *errLog) getData() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.data
}
