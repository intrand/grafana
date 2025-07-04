package dashboards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/grafana/grafana/pkg/api/dtos"
	"github.com/grafana/grafana/pkg/components/simplejson"
	"github.com/grafana/grafana/pkg/services/dashboardimport"
	"github.com/grafana/grafana/pkg/services/dashboards"
	"github.com/grafana/grafana/pkg/services/featuremgmt"
	"github.com/grafana/grafana/pkg/services/folder"
	"github.com/grafana/grafana/pkg/services/plugindashboards"
	"github.com/grafana/grafana/pkg/services/search/model"
	"github.com/grafana/grafana/pkg/tests/testinfra"
	"github.com/grafana/grafana/pkg/tests/testsuite"
	"github.com/grafana/grafana/pkg/util"
)

func TestMain(m *testing.M) {
	testsuite.Run(m)
}

func TestIntegrationDashboardQuota(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	testDashboardQuota(t, []string{})
}

func TestIntegrationDashboardQuotaK8s(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	testDashboardQuota(t, []string{featuremgmt.FlagKubernetesClientDashboardsFolders})
}

func testDashboardQuota(t *testing.T, featureToggles []string) {
	// enable quota and set low dashboard quota
	// Setup Grafana and its Database
	dashboardQuota := int64(1)
	dir, path := testinfra.CreateGrafDir(t, testinfra.GrafanaOpts{
		DisableAnonymous:     true,
		EnableQuota:          true,
		DashboardOrgQuota:    &dashboardQuota,
		EnableFeatureToggles: featureToggles,
	})

	grafanaListedAddr, _ := testinfra.StartGrafanaEnv(t, dir, path)

	t.Run("when quota limit doesn't exceed, importing a dashboard should succeed", func(t *testing.T) {
		// Import dashboard
		dashboardDataOne, err := simplejson.NewJson([]byte(`{"title":"just testing"}`))
		require.NoError(t, err)
		buf1 := &bytes.Buffer{}
		err = json.NewEncoder(buf1).Encode(dashboardimport.ImportDashboardRequest{
			Dashboard: dashboardDataOne,
		})
		require.NoError(t, err)
		u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/import", grafanaListedAddr)
		// nolint:gosec
		resp, err := http.Post(u, "application/json", buf1)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		t.Cleanup(func() {
			err := resp.Body.Close()
			require.NoError(t, err)
		})
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		dashboardDTO := &plugindashboards.PluginDashboard{}
		err = json.Unmarshal(b, dashboardDTO)
		require.NoError(t, err)
		require.EqualValues(t, 1, dashboardDTO.DashboardId)
	})

	t.Run("when quota limit exceeds importing a dashboard should fail", func(t *testing.T) {
		dashboardDataOne, err := simplejson.NewJson([]byte(`{"title":"just testing"}`))
		require.NoError(t, err)
		buf1 := &bytes.Buffer{}
		err = json.NewEncoder(buf1).Encode(dashboardimport.ImportDashboardRequest{
			Dashboard: dashboardDataOne,
		})
		require.NoError(t, err)
		u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/import", grafanaListedAddr)
		// nolint:gosec
		resp, err := http.Post(u, "application/json", buf1)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		t.Cleanup(func() {
			err := resp.Body.Close()
			require.NoError(t, err)
		})
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.JSONEq(t, `{"message":"Quota reached"}`, string(b))
	})
}

func TestIntegrationUpdatingProvisionionedDashboards(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	testUpdatingProvisionionedDashboards(t, []string{})
}

func TestIntegrationUpdatingProvisionionedDashboardsK8s(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// will be the default in g12
	testUpdatingProvisionionedDashboards(t, []string{featuremgmt.FlagKubernetesClientDashboardsFolders})
}

func testUpdatingProvisionionedDashboards(t *testing.T, featureToggles []string) {
	// Setup Grafana and its Database
	dir, path := testinfra.CreateGrafDir(t, testinfra.GrafanaOpts{
		DisableAnonymous:     true,
		EnableFeatureToggles: featureToggles,
	})

	provDashboardsDir := filepath.Join(dir, "conf", "provisioning", "dashboards")
	provDashboardsCfg := filepath.Join(provDashboardsDir, "dev.yaml")
	blob := []byte(fmt.Sprintf(`
apiVersion: 1

providers:
- name: 'provisioned dashboards'
  type: file
  allowUiUpdates: false
  options:
   path: %s`, provDashboardsDir))
	err := os.WriteFile(provDashboardsCfg, blob, 0644)
	require.NoError(t, err)
	input, err := os.ReadFile(filepath.Join("./home.json"))
	require.NoError(t, err)
	provDashboardFile := filepath.Join(provDashboardsDir, "home.json")
	err = os.WriteFile(provDashboardFile, input, 0644)
	require.NoError(t, err)
	grafanaListedAddr, _ := testinfra.StartGrafanaEnv(t, dir, path)

	// give provisioner some time since we don't have a way to know when provisioning is complete
	// TODO https://github.com/grafana/grafana/issues/85617
	time.Sleep(1 * time.Second)

	type errorResponseBody struct {
		Message string `json:"message"`
	}

	t.Run("when provisioned directory is not empty, dashboard should be created", func(t *testing.T) {
		title := "Grafana Dev Overview & Home"
		dashboardList := &model.HitList{}

		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			u := fmt.Sprintf("http://admin:admin@%s/api/search?query=%s", grafanaListedAddr, url.QueryEscape(title))
			// nolint:gosec
			resp, err := http.Get(u)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)

			b, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			err = resp.Body.Close()
			require.NoError(t, err)

			err = json.Unmarshal(b, dashboardList)
			require.NoError(t, err)

			assert.Greater(collect, dashboardList.Len(), 0, "Dashboard should be ready")
		}, 10*time.Second, 25*time.Millisecond)

		var dashboardUID string
		var dashboardID int64
		for _, d := range *dashboardList {
			dashboardUID = d.UID
			dashboardID = d.ID
		}
		assert.Equal(t, int64(1), dashboardID)

		testCases := []struct {
			desc          string
			dashboardData string
			expStatus     int
			expErrReason  string
		}{
			{
				desc:          "when updating provisioned dashboard using ID it should fail",
				dashboardData: fmt.Sprintf(`{"title":"just testing", "id": %d, "version": 1}`, dashboardID),
				expStatus:     http.StatusBadRequest,
				expErrReason:  dashboards.ErrDashboardCannotSaveProvisionedDashboard.Reason,
			},
			{
				desc:          "when updating provisioned dashboard using UID it should fail",
				dashboardData: fmt.Sprintf(`{"title":"just testing", "uid": %q, "version": 1}`, dashboardUID),
				expStatus:     http.StatusBadRequest,
				expErrReason:  dashboards.ErrDashboardCannotSaveProvisionedDashboard.Reason,
			},
			{
				desc:          "when updating dashboard using unknown ID, it should fail",
				dashboardData: `{"title":"just testing", "id": 42, "version": 1}`,
				expStatus:     http.StatusNotFound,
				expErrReason:  dashboards.ErrDashboardNotFound.Reason,
			},
			{
				desc:          "when updating dashboard using unknown UID, it should succeed",
				dashboardData: `{"title":"just testing", "uid": "unknown", "version": 1}`,
				expStatus:     http.StatusOK,
			},
		}
		for _, tc := range testCases {
			t.Run(tc.desc, func(t *testing.T) {
				u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/db", grafanaListedAddr)
				// nolint:gosec
				dashboardData, err := simplejson.NewJson([]byte(tc.dashboardData))
				require.NoError(t, err)
				buf := &bytes.Buffer{}
				err = json.NewEncoder(buf).Encode(dashboards.SaveDashboardCommand{
					Dashboard: dashboardData,
				})
				require.NoError(t, err)

				// nolint:gosec
				resp, err := http.Post(u, "application/json", buf)
				require.NoError(t, err)
				assert.Equal(t, tc.expStatus, resp.StatusCode)
				t.Cleanup(func() {
					err := resp.Body.Close()
					require.NoError(t, err)
				})
				if tc.expErrReason == "" {
					return
				}
				b, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				dashboardErr := &errorResponseBody{}
				err = json.Unmarshal(b, dashboardErr)
				require.NoError(t, err)
				assert.Equal(t, tc.expErrReason, dashboardErr.Message)
			})
		}

		t.Run("deleting provisioned dashboard should fail", func(t *testing.T) {
			u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/uid/%s", grafanaListedAddr, dashboardUID)
			req, err := http.NewRequest("DELETE", u, nil)
			if err != nil {
				fmt.Println(err)
				return
			}

			client := &http.Client{}
			resp, err := client.Do(req)
			require.NoError(t, err)
			t.Cleanup(func() {
				err := resp.Body.Close()
				require.NoError(t, err)
			})
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

			b, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			dashboardErr := &errorResponseBody{}
			err = json.Unmarshal(b, dashboardErr)
			require.NoError(t, err)
			assert.Equal(t, dashboards.ErrDashboardCannotDeleteProvisionedDashboard.Reason, dashboardErr.Message)
		})
	})
}

func TestIntegrationCreate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	testCreate(t, []string{})
}

func TestIntegrationCreateK8s(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	testCreate(t, []string{featuremgmt.FlagKubernetesClientDashboardsFolders})
}

func TestIntegrationPreserveSchemaVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	testPreserveSchemaVersion(t, []string{featuremgmt.FlagKubernetesClientDashboardsFolders})
}

func testCreate(t *testing.T, featureToggles []string) {
	// Setup Grafana and its Database
	dir, path := testinfra.CreateGrafDir(t, testinfra.GrafanaOpts{
		DisableAnonymous:     true,
		EnableFeatureToggles: featureToggles,
	})

	grafanaListedAddr, _ := testinfra.StartGrafanaEnv(t, dir, path)

	t.Run("create dashboard should succeed", func(t *testing.T) {
		dashboardDataOne, err := simplejson.NewJson([]byte(`{"title":"just testing"}`))
		require.NoError(t, err)
		buf1 := &bytes.Buffer{}
		err = json.NewEncoder(buf1).Encode(dashboards.SaveDashboardCommand{
			Dashboard: dashboardDataOne,
		})
		require.NoError(t, err)
		u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/db", grafanaListedAddr)
		// nolint:gosec
		resp, err := http.Post(u, "application/json", buf1)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		t.Cleanup(func() {
			err := resp.Body.Close()
			require.NoError(t, err)
		})
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var m util.DynMap
		err = json.Unmarshal(b, &m)
		require.NoError(t, err)
		assert.NotEmpty(t, m["id"])
		assert.NotEmpty(t, m["uid"])
	})

	t.Run("create dashboard under folder should succeed", func(t *testing.T) {
		folder := createFolder(t, grafanaListedAddr, "test folder")

		dashboardDataOne, err := simplejson.NewJson([]byte(`{"title":"just testing"}`))
		require.NoError(t, err)
		buf1 := &bytes.Buffer{}
		err = json.NewEncoder(buf1).Encode(dashboards.SaveDashboardCommand{
			Dashboard: dashboardDataOne,
			OrgID:     0,
			FolderUID: folder.UID,
		})
		require.NoError(t, err)
		u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/db", grafanaListedAddr)
		// nolint:gosec
		resp, err := http.Post(u, "application/json", buf1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		t.Cleanup(func() {
			err := resp.Body.Close()
			require.NoError(t, err)
		})
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var m util.DynMap
		err = json.Unmarshal(b, &m)
		require.NoError(t, err)
		assert.NotEmpty(t, m["id"])
		assert.NotEmpty(t, m["uid"])
		assert.Equal(t, folder.UID, m["folderUid"])
	})

	t.Run("create dashboard under folder (using deprecated folder sequential ID) should succeed", func(t *testing.T) {
		folder := createFolder(t, grafanaListedAddr, "test folder 2")

		dashboardDataOne, err := simplejson.NewJson([]byte(`{"title":"just testing"}`))
		require.NoError(t, err)
		buf1 := &bytes.Buffer{}
		err = json.NewEncoder(buf1).Encode(dashboards.SaveDashboardCommand{
			Dashboard: dashboardDataOne,
			OrgID:     0,
			FolderUID: folder.UID,
		})
		require.NoError(t, err)
		u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/db", grafanaListedAddr)
		// nolint:gosec
		resp, err := http.Post(u, "application/json", buf1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		t.Cleanup(func() {
			err := resp.Body.Close()
			require.NoError(t, err)
		})
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var m util.DynMap
		err = json.Unmarshal(b, &m)
		require.NoError(t, err)
		assert.NotEmpty(t, m["id"])
		assert.NotEmpty(t, m["uid"])
		assert.Equal(t, folder.UID, m["folderUid"])
	})

	t.Run("create dashboard under unknow folder should fail", func(t *testing.T) {
		folderUID := "unknown"
		// Import dashboard
		dashboardDataOne, err := simplejson.NewJson([]byte(`{"title":"just testing"}`))
		require.NoError(t, err)
		buf1 := &bytes.Buffer{}
		err = json.NewEncoder(buf1).Encode(dashboards.SaveDashboardCommand{
			Dashboard: dashboardDataOne,
			FolderUID: folderUID,
		})
		require.NoError(t, err)
		u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/db", grafanaListedAddr)
		// nolint:gosec
		resp, err := http.Post(u, "application/json", buf1)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		t.Cleanup(func() {
			err := resp.Body.Close()
			require.NoError(t, err)
		})
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var m util.DynMap
		err = json.Unmarshal(b, &m)
		require.NoError(t, err)
		assert.Equal(t, dashboards.ErrFolderNotFound.Error(), m["message"])
	})
}

func createFolder(t *testing.T, grafanaListedAddr string, title string) *dtos.Folder {
	t.Helper()

	buf1 := &bytes.Buffer{}
	err := json.NewEncoder(buf1).Encode(folder.CreateFolderCommand{
		Title: title,
	})
	require.NoError(t, err)
	u := fmt.Sprintf("http://admin:admin@%s/api/folders", grafanaListedAddr)
	// nolint:gosec
	resp, err := http.Post(u, "application/json", buf1)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	t.Cleanup(func() {
		err := resp.Body.Close()
		require.NoError(t, err)
	})
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var f *dtos.Folder
	err = json.Unmarshal(b, &f)
	require.NoError(t, err)

	return f
}

func intPtr(n int) *int {
	return &n
}

func testPreserveSchemaVersion(t *testing.T, featureToggles []string) {
	dir, path := testinfra.CreateGrafDir(t, testinfra.GrafanaOpts{
		DisableAnonymous:     true,
		EnableFeatureToggles: featureToggles,
	})

	grafanaListedAddr, _ := testinfra.StartGrafanaEnv(t, dir, path)

	schemaVersions := []*int{intPtr(1), intPtr(36), intPtr(40), nil}
	for _, schemaVersion := range schemaVersions {
		var title string
		if schemaVersion == nil {
			title = "save dashboard with no schemaVersion"
		} else {
			title = fmt.Sprintf("save dashboard with schemaVersion %d", *schemaVersion)
		}

		t.Run(title, func(t *testing.T) {
			// Create dashboard JSON with specified schema version
			var dashboardJSON string
			if schemaVersion != nil {
				dashboardJSON = fmt.Sprintf(`{"title":"Schema Version Test", "schemaVersion": %d}`, *schemaVersion)
			} else {
				dashboardJSON = `{"title":"Schema Version Test"}`
			}

			dashboardData, err := simplejson.NewJson([]byte(dashboardJSON))
			require.NoError(t, err)

			// Save the dashboard via API
			buf := &bytes.Buffer{}
			err = json.NewEncoder(buf).Encode(dashboards.SaveDashboardCommand{
				Dashboard: dashboardData,
			})
			require.NoError(t, err)

			url := fmt.Sprintf("http://admin:admin@%s/api/dashboards/db", grafanaListedAddr)
			// nolint:gosec
			resp, err := http.Post(url, "application/json", buf)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			t.Cleanup(func() {
				err := resp.Body.Close()
				require.NoError(t, err)
			})

			// Get dashboard UID from response
			b, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			var saveResp struct {
				UID string `json:"uid"`
			}
			err = json.Unmarshal(b, &saveResp)
			require.NoError(t, err)
			require.NotEmpty(t, saveResp.UID)

			getDashURL := fmt.Sprintf("http://admin:admin@%s/api/dashboards/uid/%s", grafanaListedAddr, saveResp.UID)
			// nolint:gosec
			getResp, err := http.Get(getDashURL)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, getResp.StatusCode)
			t.Cleanup(func() {
				err := getResp.Body.Close()
				require.NoError(t, err)
			})

			// Parse response and check if schema version is preserved
			dashBody, err := io.ReadAll(getResp.Body)
			require.NoError(t, err)

			var dashResp struct {
				Dashboard *simplejson.Json `json:"dashboard"`
			}
			err = json.Unmarshal(dashBody, &dashResp)
			require.NoError(t, err)

			actualSchemaVersion := dashResp.Dashboard.Get("schemaVersion")
			if schemaVersion != nil {
				// Check if schemaVersion is preserved (not migrated to latest)
				actualVersion := actualSchemaVersion.MustInt()
				require.Equal(t, *schemaVersion, actualVersion,
					"Dashboard schemaVersion should not be automatically changed when saved through /api/dashboards/db")
			} else {
				actualVersion, err := actualSchemaVersion.Int()
				s, _ := dashResp.Dashboard.EncodePretty()
				require.Error(t, err, fmt.Sprintf("Dashboard schemaVersion should not be automatically populated when saved through /api/dashboards/db, was %d. %s", actualVersion, string(s)))
			}
		})
	}
}

func TestIntegrationImportDashboardWithLibraryPanels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	testImportDashboardWithLibraryPanels(t, []string{})
}

func TestIntegrationImportDashboardWithLibraryPanelsK8s(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	testImportDashboardWithLibraryPanels(t, []string{featuremgmt.FlagKubernetesClientDashboardsFolders})
}

func testImportDashboardWithLibraryPanels(t *testing.T, featureToggles []string) {
	dir, path := testinfra.CreateGrafDir(t, testinfra.GrafanaOpts{
		DisableAnonymous:     true,
		EnableFeatureToggles: featureToggles,
	})

	grafanaListedAddr, _ := testinfra.StartGrafanaEnv(t, dir, path)

	t.Run("import dashboard with library panels should create library panels and connections", func(t *testing.T) {
		dashboardJSON := `{
			"title": "Test Dashboard with Library Panels",
			"panels": [
				{
					"id": 1,
					"title": "Library Panel 1",
					"type": "text",
					"gridPos": {"h": 8, "w": 12, "x": 0, "y": 0},
					"libraryPanel": {
						"uid": "test-lib-panel-1",
						"name": "Test Library Panel 1"
					}
				},
				{
					"id": 2,
					"title": "Library Panel 2", 
					"type": "stat",
					"gridPos": {"h": 8, "w": 12, "x": 12, "y": 0},
					"libraryPanel": {
						"uid": "test-lib-panel-2",
						"name": "Test Library Panel 2"
					}
				}
			],
			"__elements": {
				"test-lib-panel-1": {
					"uid": "test-lib-panel-1",
					"name": "Test Library Panel 1",
					"kind": 1,
					"type": "text",
					"model": {
						"title": "Test Library Panel 1",
						"type": "text",
						"options": {
							"content": "This is a test library panel"
						}
					}
				},
				"test-lib-panel-2": {
					"uid": "test-lib-panel-2", 
					"name": "Test Library Panel 2",
					"kind": 1,
					"type": "stat",
					"model": {
						"title": "Test Library Panel 2",
						"type": "stat",
						"options": {
							"colorMode": "value",
							"graphMode": "area",
							"justifyMode": "auto",
							"orientation": "auto",
							"reduceOptions": {
								"calcs": ["lastNotNull"],
								"fields": "",
								"values": false
							},
							"textMode": "auto"
						},
						"targets": [
							{
								"refId": "A",
								"scenarioId": "csv_metric_values",
								"stringInput": "1,20,90,30,5,0"
							}
						]
					}
				}
			}
		}`

		data, err := simplejson.NewJson([]byte(dashboardJSON))
		require.NoError(t, err)

		buf := &bytes.Buffer{}
		err = json.NewEncoder(buf).Encode(dashboardimport.ImportDashboardRequest{
			Dashboard: data,
		})
		require.NoError(t, err)

		u := fmt.Sprintf("http://admin:admin@%s/api/dashboards/import", grafanaListedAddr)
		// nolint:gosec
		resp, err := http.Post(u, "application/json", buf)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		t.Cleanup(func() {
			err := resp.Body.Close()
			require.NoError(t, err)
		})

		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var importResp struct {
			UID string `json:"uid"`
		}
		err = json.Unmarshal(b, &importResp)
		require.NoError(t, err)
		require.NotEmpty(t, importResp.UID)

		t.Run("library panels should be created", func(t *testing.T) {
			url := fmt.Sprintf("http://admin:admin@%s/api/library-elements/test-lib-panel-1", grafanaListedAddr)
			// nolint:gosec
			resp, err := http.Get(url)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			t.Cleanup(func() {
				err := resp.Body.Close()
				require.NoError(t, err)
			})

			panel, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			var panelRes struct {
				Result struct {
					UID  string `json:"uid"`
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"result"`
			}
			err = json.Unmarshal(panel, &panelRes)
			require.NoError(t, err)
			assert.Equal(t, "test-lib-panel-1", panelRes.Result.UID)
			assert.Equal(t, "Test Library Panel 1", panelRes.Result.Name)
			assert.Equal(t, "text", panelRes.Result.Type)

			url = fmt.Sprintf("http://admin:admin@%s/api/library-elements/test-lib-panel-2", grafanaListedAddr)
			// nolint:gosec
			resp, err = http.Get(url)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			t.Cleanup(func() {
				err := resp.Body.Close()
				require.NoError(t, err)
			})

			panel, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			err = json.Unmarshal(panel, &panelRes)
			require.NoError(t, err)
			assert.Equal(t, "test-lib-panel-2", panelRes.Result.UID)
			assert.Equal(t, "Test Library Panel 2", panelRes.Result.Name)
			assert.Equal(t, "stat", panelRes.Result.Type)
		})

		t.Run("library panels should be connected to dashboard", func(t *testing.T) {
			url := fmt.Sprintf("http://admin:admin@%s/api/library-elements/test-lib-panel-1/connections", grafanaListedAddr)
			// nolint:gosec
			connectionsResp, err := http.Get(url)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, connectionsResp.StatusCode)
			t.Cleanup(func() {
				err := connectionsResp.Body.Close()
				require.NoError(t, err)
			})

			connections, err := io.ReadAll(connectionsResp.Body)
			require.NoError(t, err)
			var connectionsRes struct {
				Result []struct {
					ConnectionUID string `json:"connectionUid"`
				} `json:"result"`
			}
			err = json.Unmarshal(connections, &connectionsRes)
			require.NoError(t, err)
			assert.Len(t, connectionsRes.Result, 1)
			assert.Equal(t, importResp.UID, connectionsRes.Result[0].ConnectionUID)

			url = fmt.Sprintf("http://admin:admin@%s/api/library-elements/test-lib-panel-2/connections", grafanaListedAddr)
			// nolint:gosec
			connectionsResp, err = http.Get(url)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, connectionsResp.StatusCode)
			t.Cleanup(func() {
				err := connectionsResp.Body.Close()
				require.NoError(t, err)
			})

			connections, err = io.ReadAll(connectionsResp.Body)
			require.NoError(t, err)
			err = json.Unmarshal(connections, &connectionsRes)
			require.NoError(t, err)
			assert.Len(t, connectionsRes.Result, 1)
			assert.Equal(t, importResp.UID, connectionsRes.Result[0].ConnectionUID)
		})
	})
}
