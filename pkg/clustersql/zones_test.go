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

package clustersql_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/cockroachdb/cockroach-operator/pkg/clustersql"
	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

func TestZoneConfigs(t *testing.T) {
	query := "SELECT target, full_config_yaml FROM crdb_internal.zones"

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	t.Run("returns zones from query results", func(t *testing.T) {
		expectedZones := []Zone{
			{
				Target: "us-central1-a",
				Config: ZoneConfig{
					RangeMinBytes:     1,
					RangeMaxBytes:     2,
					Replicas:          1,
					GarbageCollection: GarbageCollectionConfig{TTLSeconds: 1},
				},
			},
			{
				Target: "us-east1-b",
				Config: ZoneConfig{
					RangeMinBytes:     10,
					RangeMaxBytes:     20,
					Replicas:          3,
					GarbageCollection: GarbageCollectionConfig{TTLSeconds: 4},
				},
			},
		}

		rows := sqlmock.NewRows([]string{"target", "full_config_yaml"})
		for _, z := range expectedZones {
			yml, err := yaml.Marshal(z.Config)
			require.NoError(t, err)
			rows.AddRow(z.Target, string(yml))
		}

		mock.ExpectQuery(query).WillReturnRows(rows).RowsWillBeClosed()

		zones, err := ZoneConfigs(context.Background(), db)
		require.Equal(t, expectedZones, zones)
		require.NoError(t, err)
	})

	t.Run("returns error when query errors out", func(t *testing.T) {
		mock.ExpectQuery(query).WillReturnError(errors.New("boom"))

		zones, err := ZoneConfigs(context.Background(), db)
		require.Nil(t, zones)
		require.EqualError(t, errors.Cause(err), "boom")
	})

	t.Run("returns error when scanning fails", func(t *testing.T) {
		// second field was supposed to be YAML
		rows := sqlmock.NewRows([]string{"target", "full_config_yaml"}).AddRow("us-east1-b", 2)
		mock.ExpectQuery(query).WillReturnRows(rows)

		zones, err := ZoneConfigs(context.Background(), db)
		require.Nil(t, zones)
		require.Contains(t, err.Error(), "sql: Scan error on column index 1")
	})

	t.Run("returns error when the row stream fails after some rows", func(t *testing.T) {
		yml, err := yaml.Marshal(ZoneConfig{RangeMaxBytes: 2})
		require.NoError(t, err)
		rows := sqlmock.NewRows([]string{"target", "full_config_yaml"}).
			AddRow("us-central1-a", string(yml)).
			AddRow("us-east1-b", string(yml)).
			RowError(1, errors.New("query execution canceled"))
		mock.ExpectQuery(query).WillReturnRows(rows)

		zones, err := ZoneConfigs(context.Background(), db)
		require.Nil(t, zones)
		require.EqualError(t, errors.Cause(err), "query execution canceled")
	})
}
