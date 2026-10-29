/*
Copyright 2026 The Cockroach Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package clustersql

import (
	"context"
	"database/sql"

	"github.com/cockroachdb/errors"
	"gopkg.in/yaml.v2"
)

// GarbageCollectionConfig struct
type GarbageCollectionConfig struct {
	TTLSeconds uint `yaml:"ttlseconds"`
}

// ZoneConfig struct
type ZoneConfig struct {
	RangeMinBytes     uint64                  `yaml:"range_min_bytes"`
	RangeMaxBytes     uint64                  `yaml:"range_max_bytes"`
	Replicas          uint                    `yaml:"num_replicas"`
	GarbageCollection GarbageCollectionConfig `yaml:"gc"`
}

// Scan func
func (c *ZoneConfig) Scan(value interface{}) error {
	bytes, ok := value.(string)
	if !ok {
		return errors.Errorf("expected string got %T", value)
	}
	return yaml.Unmarshal([]byte(bytes), c)
}

// Zone struct
type Zone struct {
	Target string
	Config ZoneConfig
}

// ZoneConfigs func
func ZoneConfigs(ctx context.Context, db *sql.DB) ([]Zone, error) {
	// TODO (chrisseto): Will we ever need additional fields??
	rows, err := db.QueryContext(ctx, `SELECT target, full_config_yaml FROM crdb_internal.zones`)
	if err != nil {
		return nil, errors.Wrap(err, "failed to select from crdb_internal.zones")
	}
	defer rows.Close()

	var zones []Zone
	for rows.Next() {
		var zone Zone
		if err := rows.Scan(&zone.Target, &zone.Config); err != nil {
			return nil, errors.Wrap(err, "failed to scan rows")
		}
		zones = append(zones, zone)
	}
	// Next also returns false when the stream fails, which would otherwise look
	// like a short but complete result set.
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "failed to iterate crdb_internal.zones")
	}
	return zones, nil
}
