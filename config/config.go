// Copyright 2017-2023 The Cloudprober Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cloud.google.com/go/compute/metadata"
	configpb "github.com/cloudprober/cloudprober/config/proto"
	"github.com/cloudprober/cloudprober/internal/file"
	"github.com/cloudprober/cloudprober/logger"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"sigs.k8s.io/yaml"
)

var (
	configFile = flag.String("config_file", "", "Config file")
)

// EnvRegex is the regex used to find environment variable placeholders
// in the config file. The placeholders are of the form **$<env_var_name>**,
// and are added during Go template processing for envSecret functions.
var EnvRegex = regexp.MustCompile(`\*\*\$([^*\s]+)\*\*`)

const (
	configMetadataKeyName = "cloudprober_config"
	defaultConfigFile     = "/etc/cloudprober.cfg"
)

func readConfigFile(fileName string) (string, string, error) {
	b, err := file.ReadFile(fileName)
	if err != nil {
		return "", "", err
	}

	switch filepath.Ext(fileName) {
	case ".pb.txt", ".cfg", ".textpb":
		return string(b), "textpb", nil
	case ".json":
		return string(b), "json", nil
	case ".yaml", ".yml":
		return string(b), "yaml", nil
	}

	return string(b), "", nil
}

func GetConfig(confFile string, l *logger.Logger) (content string, format string, err error) {
	if confFile != "" {
		return readConfigFile(confFile)
	}

	if *configFile != "" {
		return readConfigFile(*configFile)
	}

	// On GCE first check if there is a config in custom metadata
	// attributes.
	if metadata.OnGCE() {
		if config, err := ReadFromGCEMetadata(configMetadataKeyName); err != nil {
			l.Infof("Error reading config from metadata. Err: %v", err)
		} else {
			return config, "", nil
		}
	}

	// If config not found in metadata, check default config on disk
	if _, err := os.Stat(defaultConfigFile); !os.IsNotExist(err) {
		return readConfigFile(defaultConfigFile)
	}

	l.Warningf("Config file %s not found. Using default config.", defaultConfigFile)
	return DefaultConfig(), "textpb", nil
}

func configToProto(configStr, configFormat string) (*configpb.ProberConfig, error) {
	cfg := &configpb.ProberConfig{}
	switch configFormat {
	case "yaml":
		jsonCfg, err := yaml.YAMLToJSON([]byte(configStr))
		if err != nil {
			return nil, fmt.Errorf("error converting YAML config to JSON: %v", err)
		}
		if err := protojson.Unmarshal(jsonCfg, cfg); err != nil {
			return nil, fmt.Errorf("error unmarshaling intermediate JSON to proto: %v", err)
		}
	case "json":
		if err := protojson.Unmarshal([]byte(configStr), cfg); err != nil {
			return nil, err
		}
	default:
		if err := prototext.Unmarshal([]byte(configStr), cfg); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

func ConfigTest(fileName string, baseVars map[string]string) error {
	if fileName == "" {
		fileName = *configFile
	}
	content, configFormat, err := readConfigFile(fileName)
	if err != nil {
		return err
	}

	configStr, err := ParseTemplate(content, baseVars, func(v string) (string, error) {
		return v + "-test-value", nil
	})

	if err != nil {
		return err
	}

	_, err = configToProto(configStr, configFormat)
	return err
}

func DumpConfig(fileName, outFormat string, baseVars map[string]string) ([]byte, error) {
	if fileName == "" {
		fileName = *configFile
	}

	content, configFormat, err := readConfigFile(fileName)
	if err != nil {
		return nil, err
	}

	cfg, _, err := ParseConfig(content, configFormat, baseVars, nil)
	if err != nil {
		return nil, err
	}

	switch outFormat {
	case "yaml":
		jsonCfg, err := protojson.Marshal(cfg)
		if err != nil {
			return nil, fmt.Errorf("error converting config to json: %v", err)
		}
		return yaml.JSONToYAML(jsonCfg)
	case "json":
		return protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(cfg)
	case "textpb":
		return prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(cfg)
	default:
		return nil, fmt.Errorf("unknown format: %s", outFormat)
	}
}

// substEnvVars substitutes environment variables in the config string.
func substEnvVars(configStr string, l *logger.Logger) string {
	m := EnvRegex.FindAllStringSubmatch(configStr, -1)
	if len(m) == 0 {
		return configStr
	}

	var envVars []string
	for _, match := range m {
		if len(match) != 2 {
			continue
		}
		fmt.Printf("Found env var: %v\n", match)
		envVars = append(envVars, match[1]) // match[0] is the whole string.
	}

	for _, v := range envVars {
		envVal := os.Getenv(v)
		if envVal == "" {
			l.Warningf("Environment variable %s not defined, skipping substitution.", v)
			continue
		}
		configStr = strings.ReplaceAll(configStr, "**$"+v+"**", envVal)
	}

	return configStr
}

func ParseConfig(content, format string, vars map[string]string, l *logger.Logger) (*configpb.ProberConfig, string, error) {
	parsedConfig, err := ParseTemplate(content, vars, nil)
	if err != nil {
		return nil, "", fmt.Errorf("error parsing config file as Go template. Err: %v", err)
	}

	cfg, err := configToProto(substEnvVars(parsedConfig, l), format)
	return cfg, parsedConfig, err
}
