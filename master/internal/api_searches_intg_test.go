//go:build integration
// +build integration

package internal

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/experiment"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/schemas"
	"github.com/determined-ai/determined/master/pkg/schemas/expconf"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/trialv1"
)

// experimentMock inherits all the methods of the Experiment
// interface, but only implements the ones we care about
// for testing.
type experimentMock struct {
	mock.Mock
	experiment.Experiment
}

func (m *experimentMock) ActivateExperiment() error {
	returns := m.Called()
	return returns.Error(0)
}

func (m *experimentMock) PauseExperiment() error {
	returns := m.Called()
	return returns.Error(0)
}

// nolint: exhaustruct
func createTestSearchWithHParams(
	t *testing.T, api *apiServer, curUser model.User, projectID int, hparams map[string]any,
) *model.Experiment {
	experimentConfig := expconf.ExperimentConfig{
		RawDescription: ptrs.Ptr("desc"),
		RawName:        expconf.Name{RawString: ptrs.Ptr("name")},
	}

	b, err := json.Marshal(hparams)
	require.NoError(t, err)
	err = json.Unmarshal(b, &experimentConfig.RawHyperparameters)
	require.NoError(t, err)

	activeConfig := schemas.WithDefaults(schemas.Merge(minExpConfig, experimentConfig))
	return createTestExpWithActiveConfig(t, api, curUser, projectID, activeConfig)
}

func TestMoveSearchesIds(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	sourceprojectID := int32(1)
	destprojectID := int32(projectIDInt)

	search1 := createTestExp(t, api, curUser)
	search2 := createTestExp(t, api, curUser)

	moveIds := []int32{int32(search1.ID)}

	moveReq := &apiv1.MoveSearchesRequest{
		SearchIds:            moveIds,
		SourceProjectId:      sourceprojectID,
		DestinationProjectId: destprojectID,
	}

	moveResp, err := api.MoveSearches(ctx, moveReq)
	require.NoError(t, err)
	require.Len(t, moveResp.Results, 1)
	require.Equal(t, "", moveResp.Results[0].Error)

	// run no longer in old project
	filter := fmt.Sprintf(`{"filterGroup":{"children":[{"columnName":"id","kind":"field",`+
		`"location":"LOCATION_TYPE_EXPERIMENT","operator":"=","type":"COLUMN_TYPE_NUMBER","value":%d}],`+
		`"conjunction":"and","kind":"group"},"showArchived":false}`, int32(search2.ID))
	req := &apiv1.SearchExperimentsRequest{
		ProjectId: &sourceprojectID,
		Filter:    &filter,
	}
	resp, err := api.SearchExperiments(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Experiments, 1)
	require.Equal(t, int32(search2.ID), resp.Experiments[0].Experiment.Id)

	// runs in new project
	req = &apiv1.SearchExperimentsRequest{
		ProjectId: &destprojectID,
		Sort:      ptrs.Ptr("id=desc"),
	}

	resp, err = api.SearchExperiments(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Experiments, 1)
	require.Equal(t, moveIds[0], resp.Experiments[0].Experiment.Id)
}

func TestMoveSearchesSameIds(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	sourceprojectID := int32(1)

	search1 := createTestExp(t, api, curUser)
	moveIds := []int32{int32(search1.ID)}

	moveReq := &apiv1.MoveSearchesRequest{
		SearchIds:            moveIds,
		SourceProjectId:      sourceprojectID,
		DestinationProjectId: sourceprojectID,
	}

	moveResp, err := api.MoveSearches(ctx, moveReq)
	require.NoError(t, err)
	require.Empty(t, moveResp.Results)
}

func TestMoveSearchesFilter(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	_, projectID2Int := createProjectAndWorkspace(ctx, t, api)
	sourceprojectID := int32(projectIDInt)
	destprojectID := int32(projectID2Int)

	hyperparameters1 := map[string]any{"global_batch_size": 1, "test1": map[string]any{"test2": 1}}
	hyperparameters2 := map[string]any{"test1": map[string]any{"test2": 5}}
	exp1 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters1)
	exp2 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters2)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.PausedState,
		ExperimentID: exp1.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters1,
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.PausedState,
		ExperimentID: exp2.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters2,
	}, task2.TaskID))

	projHparam := getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 2)
	require.True(t, slices.Contains(projHparam, "test1.test2"))
	require.True(t, slices.Contains(projHparam, "global_batch_size"))

	req := &apiv1.SearchRunsRequest{
		ProjectId: &sourceprojectID,
		Sort:      ptrs.Ptr("id=asc"),
	}
	_, err := api.SearchRuns(ctx, req)
	require.NoError(t, err)

	// If provided with filter MoveSearches should ignore these move ids
	moveIds := []int32{int32(exp1.ID), int32(exp2.ID)}

	moveReq := &apiv1.MoveSearchesRequest{
		SearchIds:            moveIds,
		SourceProjectId:      sourceprojectID,
		DestinationProjectId: destprojectID,
		Filter: ptrs.Ptr(`{"filterGroup":{"children":[{"columnName":"hp.test1.test2","kind":"field",` +
			`"location":"LOCATION_TYPE_HYPERPARAMETERS","operator":"<=","type":"COLUMN_TYPE_NUMBER","value":1}],` +
			`"conjunction":"and","kind":"group"},"showArchived":false}`),
	}

	moveResp, err := api.MoveSearches(ctx, moveReq)
	require.NoError(t, err)
	require.Len(t, moveResp.Results, 1)
	require.Equal(t, "", moveResp.Results[0].Error)

	// check 1 run moved in old project
	resp, err := api.SearchRuns(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Runs, 1)

	// run in new project
	req = &apiv1.SearchRunsRequest{
		ProjectId: &destprojectID,
		Sort:      ptrs.Ptr("id=asc"),
	}

	resp, err = api.SearchRuns(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Runs, 1)

	// Hyperparam moved out of project A
	projHparam = getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 1)
	require.Equal(t, "test1.test2", projHparam[0])

	// Hyperparams moved into project B
	projHparam = getTestProjectHyperparmeters(ctx, t, projectID2Int)
	require.Len(t, projHparam, 2)
	require.True(t, slices.Contains(projHparam, "test1.test2"))
	require.True(t, slices.Contains(projHparam, "global_batch_size"))

	i := strings.Index(resp.Runs[0].LocalId, "-")
	localID := resp.Runs[0].LocalId[i+1:]
	require.Equal(t, "1", localID)
}

func TestDeleteSearchesNonTerminal(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	exp := createTestExpWithProjectID(t, api, curUser, projectIDInt)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task2.TaskID))

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Sort:      ptrs.Ptr("id=asc"),
	}
	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)

	searchIDs := []int32{int32(exp.ID)}
	req := &apiv1.DeleteSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	res, err := api.DeleteSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "Search is not in a terminal state.", res.Results[0].Error)

	searchReq = &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err = api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)
}

func TestDeleteSearchesIds(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	projectID, _, _, _, expID := setUpMultiTrialExperiments(ctx, t, api, curUser) //nolint:dogsled
	require.NoError(t, completeExp(ctx, expID))

	expIDs := []int32{expID}
	req := &apiv1.DeleteSearchesRequest{
		SearchIds: expIDs,
		ProjectId: projectID,
	}
	res, err := api.DeleteSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "", res.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Empty(t, searchResp.Runs)
}

func TestDeleteSearchesIdsNonExistent(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	// delete runs
	searchIDs := []int32{-1}
	req := &apiv1.DeleteSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	res, err := api.DeleteSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, fmt.Sprintf("Search with id '%d' not found in project with id '%d'", -1, projectID),
		res.Results[0].Error)
}

func TestDeleteSearchesFilter(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	hyperparameters1 := map[string]any{"global_batch_size": 1, "test1": map[string]any{"test2": 1}}
	exp1 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters1)
	hyperparameters2 := map[string]any{"test1": map[string]any{"test2": 5}}
	exp2 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters2)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp1.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters1,
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp2.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters2,
	}, task2.TaskID))

	projHparam := getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 2)
	require.True(t, slices.Contains(projHparam, "test1.test2"))
	require.True(t, slices.Contains(projHparam, "global_batch_size"))

	require.NoError(t, completeExp(ctx, int32(exp1.ID)))
	require.NoError(t, completeExp(ctx, int32(exp2.ID)))

	filter := `{
		"filterGroup": {
		  "children": [
			{
			  "columnName": "hp.test1.test2",
			  "kind": "field",
			  "location": "LOCATION_TYPE_HYPERPARAMETERS",
			  "operator": "<=",
			  "type": "COLUMN_TYPE_NUMBER",
			  "value": 1
			}
		  ],
		  "conjunction": "and",
		  "kind": "group"
		},
		"showArchived": true
	  }`
	req := &apiv1.DeleteSearchesRequest{
		Filter:    &filter,
		ProjectId: projectID,
	}
	res, err := api.DeleteSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "", res.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	projHparam = getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 1)
	require.Equal(t, "test1.test2", projHparam[0])

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 1)
}

func TestCancelSearchesNonTerminal(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	exp := createTestExpWithProjectID(t, api, curUser, projectIDInt)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task2.TaskID))

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Sort:      ptrs.Ptr("id=asc"),
	}
	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)

	searchIDs := []int32{int32(exp.ID)}
	req := &apiv1.CancelSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	res, err := api.CancelSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Empty(t, res.Results[0].Error)
	require.Equal(t, res.Results[0].Id, searchIDs[0])

	searchReq = &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err = api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)
}

func TestCancelSearchesIds(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	projectID, _, _, _, expID := setUpMultiTrialExperiments(ctx, t, api, curUser) //nolint:dogsled

	expIDs := []int32{expID}
	req := &apiv1.CancelSearchesRequest{
		SearchIds: expIDs,
		ProjectId: projectID,
	}
	res, err := api.CancelSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "", res.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.GetRuns(), 2)
	for _, run := range searchResp.GetRuns() {
		require.Equal(t, trialv1.State_STATE_COMPLETED, run.State)
	}
}

func TestCancelSearchesIdsNonExistent(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	// cancel runs
	searchIDs := []int32{-1}
	req := &apiv1.CancelSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	res, err := api.CancelSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, fmt.Sprintf("Search with id '%d' not found", -1),
		res.Results[0].Error)
}

func TestCancelSearchesFilter(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	hyperparameters1 := map[string]any{"global_batch_size": 1, "test3": map[string]any{"test4": 1}}
	exp1 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters1)
	hyperparameters2 := map[string]any{"test3": map[string]any{"test4": 5}}
	exp2 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters2)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp1.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters1,
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp2.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters2,
	}, task2.TaskID))

	projHparam := getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 2)
	require.True(t, slices.Contains(projHparam, "test3.test4"))
	require.True(t, slices.Contains(projHparam, "global_batch_size"))

	require.NoError(t, completeExp(ctx, int32(exp1.ID)))
	require.NoError(t, completeExp(ctx, int32(exp2.ID)))

	filter := `{
		"filterGroup": {
		  "children": [
			{
			  "columnName": "hp.test3.test4",
			  "kind": "field",
			  "location": "LOCATION_TYPE_HYPERPARAMETERS",
			  "operator": "<",
			  "type": "COLUMN_TYPE_NUMBER",
			  "value": 2
			}
		  ],
		  "conjunction": "and",
		  "kind": "group"
		},
		"showArchived": true
	  }`
	req := &apiv1.CancelSearchesRequest{
		Filter:    &filter,
		ProjectId: projectID,
	}
	res, err := api.CancelSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "", res.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	projHparam = getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 2)
	require.Contains(t, projHparam, "test3.test4")
	require.Contains(t, projHparam, "global_batch_size")

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.GetRuns(), 2)
}

func TestKillSearchesNonTerminal(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	exp := createTestExpWithProjectID(t, api, curUser, projectIDInt)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task2.TaskID))

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Sort:      ptrs.Ptr("id=asc"),
	}
	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.GetRuns(), 2)

	searchIDs := []int32{int32(exp.ID)}
	req := &apiv1.KillSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	res, err := api.KillSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Empty(t, res.Results[0].Error)
	require.Equal(t, res.Results[0].Id, searchIDs[0])

	searchReq = &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err = api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)
}

func TestKillSearchesMixedStates(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	nonTerminalExp := createTestExpWithProjectID(t, api, curUser, projectIDInt)
	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: nonTerminalExp.ID,
		StartTime:    time.Now(),
	}, task1.TaskID))

	terminalExp := createTestExpWithProjectID(t, api, curUser, projectIDInt)
	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: terminalExp.ID,
		StartTime:    time.Now(),
	}, task2.TaskID))
	require.NoError(t, completeExp(ctx, int32(terminalExp.ID)))

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Sort:      ptrs.Ptr("id=asc"),
	}
	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.GetRuns(), 2)

	searchIDs := []int32{int32(nonTerminalExp.ID), int32(terminalExp.ID), int32(-1)}
	req := &apiv1.KillSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	res, err := api.KillSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 3)
	require.Contains(t, res.Results, &apiv1.SearchActionResult{
		Error: "",
		Id:    int32(nonTerminalExp.ID),
	})
	require.Contains(t, res.Results, &apiv1.SearchActionResult{
		Error: "",
		Id:    int32(terminalExp.ID),
	})
	require.Contains(t, res.Results, &apiv1.SearchActionResult{
		Error: fmt.Sprintf("Search with id '%d' not found", -1),
		Id:    int32(-1),
	})

	searchReq = &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err = api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)
}

func TestKillSearchesIds(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	projectID, _, _, _, expID := setUpMultiTrialExperiments(ctx, t, api, curUser) //nolint:dogsled

	expIDs := []int32{expID}
	req := &apiv1.KillSearchesRequest{
		SearchIds: expIDs,
		ProjectId: projectID,
	}
	res, err := api.KillSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "", res.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.GetRuns(), 2)
	for _, run := range searchResp.GetRuns() {
		require.Equal(t, trialv1.State_STATE_COMPLETED, run.State)
	}
}

func TestKillSearchesIdsNonExistent(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	// delete runs
	searchIDs := []int32{-1}
	req := &apiv1.KillSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	res, err := api.KillSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, fmt.Sprintf("Search with id '%d' not found", -1),
		res.Results[0].Error)
}

func TestKillSearchesFilter(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	hyperparameters1 := map[string]any{"global_batch_size": 1, "test5": map[string]any{"test6": 1}}
	exp1 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters1)
	hyperparameters2 := map[string]any{"test5": map[string]any{"test6": 5}}
	exp2 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters2)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp1.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters1,
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp2.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters2,
	}, task2.TaskID))

	projHparam := getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 2)
	require.Contains(t, projHparam, "test5.test6")
	require.Contains(t, projHparam, "global_batch_size")

	filter := `{
		"filterGroup": {
		  "children": [
			{
			  "columnName": "hp.test5.test6",
			  "kind": "field",
			  "location": "LOCATION_TYPE_HYPERPARAMETERS",
			  "operator": "<=",
			  "type": "COLUMN_TYPE_NUMBER",
			  "value": 1
			}
		  ],
		  "conjunction": "and",
		  "kind": "group"
		},
		"showArchived": true
	  }`
	req := &apiv1.KillSearchesRequest{
		Filter:    &filter,
		ProjectId: projectID,
	}
	res, err := api.KillSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "", res.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	projHparam = getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 2)
	require.Contains(t, projHparam, "test5.test6")
	require.Contains(t, projHparam, "global_batch_size")

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.GetRuns(), 2)
}

func TestPauseSearchesNonTerminal(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	exp := createTestExpWithProjectID(t, api, curUser, projectIDInt)
	require.Equal(t, model.PausedState, exp.State)
	mock := experimentMock{}
	mock.On("ActivateExperiment").Return(nil)
	mock.On("PauseExperiment").Return(nil)
	require.NoError(t, experiment.ExperimentRegistry.Add(exp.ID, &mock))

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.PausedState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.PausedState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task2.TaskID))

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Sort:      ptrs.Ptr("id=asc"),
	}
	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)

	searchIDs := []int32{int32(exp.ID)}
	resumeResp, err := api.ResumeSearches(ctx, &apiv1.ResumeSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	})
	require.NoError(t, err)
	require.Len(t, resumeResp.Results, 1)
	require.Empty(t, resumeResp.Results[0].Error)
	require.Equal(t, resumeResp.Results[0].Id, searchIDs[0])
	pauseResp, err := api.PauseSearches(ctx, &apiv1.PauseSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	})
	require.NoError(t, err)
	require.Len(t, pauseResp.Results, 1)
	require.Empty(t, pauseResp.Results[0].Error)
	require.Equal(t, pauseResp.Results[0].Id, searchIDs[0])

	searchReq = &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err = api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)
}

func TestPauseSearchesIds(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	terminalExp := createTestExpWithProjectID(t, api, curUser, projectIDInt)
	require.NoError(t, completeExp(ctx, int32(terminalExp.ID)))

	nonTerminalExp := createTestExpWithProjectID(t, api, curUser, projectIDInt)
	mock := experimentMock{}
	mock.On("ActivateExperiment").Return(nil)
	mock.On("PauseExperiment").Return(nil)
	require.NoError(t, experiment.ExperimentRegistry.Add(nonTerminalExp.ID, &mock))

	expIDs := []int32{int32(terminalExp.ID), int32(nonTerminalExp.ID)}
	req := &apiv1.PauseSearchesRequest{
		SearchIds: expIDs,
		ProjectId: projectID,
	}
	res, err := api.PauseSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 2)
	require.Contains(t, res.Results, &apiv1.SearchActionResult{
		Error: "",
		Id:    int32(nonTerminalExp.ID),
	})
	require.Contains(t, res.Results, &apiv1.SearchActionResult{
		Error: "Failed to pause experiment: rpc error: code = FailedPrecondition desc = experiment in terminal state",
		Id:    int32(terminalExp.ID),
	})
}

func TestPauseSearchesIdsNonExistent(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	// cancel runs
	searchIDs := []int32{-1}
	req := &apiv1.PauseSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	res, err := api.PauseSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, fmt.Sprintf("Search with id '%d' not found in project with id '%d'", -1, projectID),
		res.Results[0].Error)
}

func TestPauseSearchesFilter(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	hyperparameters1 := map[string]any{"global_batch_size": 1, "test3": map[string]any{"test4": 1}}
	exp1 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters1)
	mock := experimentMock{}
	mock.On("ActivateExperiment").Return(nil)
	mock.On("PauseExperiment").Return(nil)
	require.NoError(t, experiment.ExperimentRegistry.Add(exp1.ID, &mock))

	hyperparameters2 := map[string]any{"test3": map[string]any{"test4": 5}}
	exp2 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters2)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp1.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters1,
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: exp2.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters2,
	}, task2.TaskID))

	projHparam := getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 2)
	require.True(t, slices.Contains(projHparam, "test3.test4"))
	require.True(t, slices.Contains(projHparam, "global_batch_size"))

	filter := `{
		"filterGroup": {
		  "children": [
			{
			  "columnName": "hp.test3.test4",
			  "kind": "field",
			  "location": "LOCATION_TYPE_HYPERPARAMETERS",
			  "operator": "<=",
			  "type": "COLUMN_TYPE_NUMBER",
			  "value": 1
			}
		  ],
		  "conjunction": "and",
		  "kind": "group"
		},
		"showArchived": true
	  }`
	req := &apiv1.PauseSearchesRequest{
		Filter:    &filter,
		ProjectId: projectID,
	}
	res, err := api.PauseSearches(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "", res.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":true}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	projHparam = getTestProjectHyperparmeters(ctx, t, projectIDInt)
	require.Len(t, projHparam, 2)
	require.Contains(t, projHparam, "test3.test4")
	require.Contains(t, projHparam, "global_batch_size")

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.GetRuns(), 2)
}

func TestArchiveUnarchiveSearchIds(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	projectID, _, _, _, expID := setUpMultiTrialExperiments(ctx, t, api, curUser) //nolint:dogsled
	require.NoError(t, completeExp(ctx, expID))

	searchIDs := []int32{expID}
	archReq := &apiv1.ArchiveSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	archRes, err := api.ArchiveSearches(ctx, archReq)
	require.NoError(t, err)
	require.Len(t, archRes.Results, 1)
	require.Equal(t, "", archRes.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":false}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Empty(t, searchResp.Runs)

	// Unarchive runs
	unarchReq := &apiv1.UnarchiveSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	unarchRes, err := api.UnarchiveSearches(ctx, unarchReq)
	require.NoError(t, err)
	require.Len(t, unarchRes.Results, 1)
	require.Equal(t, "", unarchRes.Results[0].Error)

	searchResp, err = api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)
}

func TestArchiveUnarchiveSearchFilter(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	hyperparameters1 := map[string]any{"global_batch_size": 1, "test1": map[string]any{"test2": 1}}
	exp1 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters1)
	hyperparameters2 := map[string]any{"global_batch_size": 1, "test1": map[string]any{"test2": 5}}
	exp2 := createTestSearchWithHParams(t, api, curUser, projectIDInt, hyperparameters2)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp1.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters1,
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp2.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters2,
	}, task2.TaskID))

	require.NoError(t, completeExp(ctx, int32(exp1.ID)))
	require.NoError(t, completeExp(ctx, int32(exp2.ID)))

	filter := `{"filterGroup":{"children":[{"columnName":"hp.test1.test2","kind":"field",` +
		`"location":"LOCATION_TYPE_HYPERPARAMETERS","operator":"<=","type":"COLUMN_TYPE_NUMBER","value":1}],` +
		`"conjunction":"and","kind":"group"},"showArchived":true}`
	archReq := &apiv1.ArchiveSearchesRequest{
		Filter:    &filter,
		ProjectId: projectID,
	}
	archRes, err := api.ArchiveSearches(ctx, archReq)
	require.NoError(t, err)
	require.Len(t, archRes.Results, 1)
	require.Equal(t, "", archRes.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":false}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 1)

	// Unarchive runs
	unarchReq := &apiv1.UnarchiveSearchesRequest{
		Filter:    &filter,
		ProjectId: projectID,
	}
	unarchRes, err := api.UnarchiveSearches(ctx, unarchReq)
	require.NoError(t, err)
	require.Len(t, unarchRes.Results, 1)
	require.Equal(t, "", unarchRes.Results[0].Error)

	searchResp, err = api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Len(t, searchResp.Runs, 2)
}

func TestArchiveAlreadyArchivedSearch(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	projectID, _, _, _, expID := setUpMultiTrialExperiments(ctx, t, api, curUser) //nolint:dogsled
	require.NoError(t, completeExp(ctx, expID))

	// Archive runs
	searchIDs := []int32{expID}
	archReq := &apiv1.ArchiveSearchesRequest{
		SearchIds: searchIDs,
		ProjectId: projectID,
	}
	archRes, err := api.ArchiveSearches(ctx, archReq)
	require.NoError(t, err)
	require.Len(t, archRes.Results, 1)
	require.Equal(t, "", archRes.Results[0].Error)

	searchReq := &apiv1.SearchRunsRequest{
		ProjectId: &projectID,
		Filter:    ptrs.Ptr(`{"showArchived":false}`),
		Sort:      ptrs.Ptr("id=asc"),
	}

	searchResp, err := api.SearchRuns(ctx, searchReq)
	require.NoError(t, err)
	require.Empty(t, searchResp.Runs)

	// Try to archive again
	archRes, err = api.ArchiveSearches(ctx, archReq)
	require.NoError(t, err)
	require.Len(t, archRes.Results, 1)
	require.Equal(t, "", archRes.Results[0].Error)
}

func TestArchiveSearchNonTerminalState(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	_, projectIDInt := createProjectAndWorkspace(ctx, t, api)
	projectID := int32(projectIDInt)

	exp := createTestExpWithProjectID(t, api, curUser, projectIDInt)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.ActiveState,
		ExperimentID: exp.ID,
		StartTime:    time.Now(),
	}, task1.TaskID))

	archReq := &apiv1.ArchiveSearchesRequest{
		SearchIds: []int32{int32(exp.ID)},
		ProjectId: projectID,
	}
	archRes, err := api.ArchiveSearches(ctx, archReq)
	require.NoError(t, err)
	require.Len(t, archRes.Results, 1)
	require.Equal(t, "Search is not in terminal state.", archRes.Results[0].Error)
}

func TestUnarchiveSearchAlreadyUnarchived(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	projectID, _, _, _, exp := setUpMultiTrialExperiments(ctx, t, api, curUser) //nolint:dogsled
	require.NoError(t, completeExp(ctx, exp))

	unarchReq := &apiv1.UnarchiveSearchesRequest{
		SearchIds: []int32{exp},
		ProjectId: projectID,
	}
	unarchRes, err := api.UnarchiveSearches(ctx, unarchReq)
	require.NoError(t, err)
	require.Len(t, unarchRes.Results, 1)
	require.Equal(t, "", unarchRes.Results[0].Error)
}

func TestGetSearchIdsFromFilter(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)
	workspaceID, projectID := createProjectAndWorkspace(ctx, t, api)

	hyperparameters1 := map[string]any{"global_batch_size": 6, "test7": map[string]any{"test8": 9}}
	exp1 := createTestSearchWithHParams(t, api, curUser, projectID, hyperparameters1)
	hyperparameters2 := map[string]any{"global_batch_size": 10, "test7": map[string]any{"test8": 11}}
	exp2 := createTestSearchWithHParams(t, api, curUser, projectID, hyperparameters2)

	task1 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task1))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp1.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters1,
	}, task1.TaskID))

	task2 := &model.Task{TaskType: model.TaskTypeTrial, TaskID: model.NewTaskID()}
	require.NoError(t, db.AddTask(ctx, task2))
	require.NoError(t, db.AddTrial(ctx, &model.Trial{
		State:        model.CompletedState,
		ExperimentID: exp2.ID,
		StartTime:    time.Now(),
		HParams:      hyperparameters2,
	}, task2.TaskID))

	filter := `{"filterGroup":{"children":[{"columnName":"hp.test7.test8","kind":"field",` +
		`"location":"LOCATION_TYPE_HYPERPARAMETERS","operator":"<=","type":"COLUMN_TYPE_NUMBER","value":10}],` +
		`"conjunction":"and","kind":"group"},"showArchived":true}`
	searchIds, err := getSearchIdsFromFilter(ctx, int32(workspaceID), &filter)
	require.NoError(t, err)
	require.ElementsMatch(t, searchIds, []int32{int32(exp1.ID)})
}
