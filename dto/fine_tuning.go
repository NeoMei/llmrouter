package dto

type FineTuningCreateRequest struct {
	TrainingFile    string         `json:"training_file"`
	ValidationFile  string         `json:"validation_file,omitempty"`
	Model           string         `json:"model"`
	Suffix          string         `json:"suffix,omitempty"`
	Hyperparameters map[string]any `json:"hyperparameters,omitempty"`
	NEpochs         any            `json:"n_epochs,omitempty"`
}

type FineTuningJobObject struct {
	ID              string         `json:"id"`
	Object          string         `json:"object"`
	Model           string         `json:"model"`
	CreatedAt       int64          `json:"created_at"`
	FinishedAt      int64          `json:"finished_at,omitempty"`
	FineTunedModel  string         `json:"fine_tuned_model,omitempty"`
	Status          string         `json:"status"`
	TrainingFile    string         `json:"training_file"`
	ValidationFile  string         `json:"validation_file,omitempty"`
	Hyperparameters map[string]any `json:"hyperparameters,omitempty"`
	FailureReason   any            `json:"error,omitempty"`
}

type FineTuningJobList struct {
	Object string                `json:"object"`
	Data   []FineTuningJobObject `json:"data"`
}

type FineTuningEventObject struct {
	Object    string `json:"object"`
	CreatedAt int64  `json:"created_at"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
}

type FineTuningEventList struct {
	Object string                  `json:"object"`
	Data   []FineTuningEventObject `json:"data"`
}

type ModelDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}
