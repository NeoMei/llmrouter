package ocr

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/gateway"
	"github.com/QuantumNous/new-api/relay/channel/paddleocr"
	base "github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/service/objectstore"
	"gorm.io/gorm"
)

const OCROperation = "ocr.jobs.create"

// Target is trusted, authorized catalog data, never decoded directly from a
// client. Cross-channel fallback after intent creation is deliberately forbidden.
type Target struct {
	JobsURL, Model string
	ProfileID      int
}
type CreateRequest struct {
	Admission AuthorizedAdmission
	Target    Target
	Input     Input
	// Remaining actual-send budget after any caller activity. 0 uses RetryTimes+1.
	MaxAttempts  int
	DisableRetry bool
}
type Config struct {
	PublicOrigin string
	// Persistent server secret, independent of user/provider keys, >=32 bytes.
	GrantSecret []byte
	GrantTTL    time.Duration
	Retention   time.Duration
	Now         func() time.Time
	Objects     objectstore.Store
	GatewayKeys *gateway.KeySelector
	// Test/custom transport injection is trusted server configuration only.
	SupplierHTTP *http.Client
	FetchHTTP    *http.Client
}
type Service struct {
	db           *gorm.DB
	billing      *Billing
	objects      objectstore.Store
	keys         *gateway.KeySelector
	supplier     paddleocr.Client
	fetchClient  *http.Client
	grantSecret  []byte
	publicOrigin string
	grantTTL     time.Duration
	retention    time.Duration
	now          func() time.Time
}

func NewService(db *gorm.DB, billing *Billing, c Config) (*Service, error) {
	origin, err := validateOrigin(c.PublicOrigin)
	if err != nil {
		return nil, err
	}
	if db == nil || billing == nil || c.Objects == nil || len(c.GrantSecret) < 32 {
		return nil, errors.New("OCR database, billing, object store and persistent grant secret required")
	}
	ttl := c.GrantTTL
	if ttl == 0 {
		ttl = time.Hour
	}
	if ttl < time.Minute || ttl > time.Hour {
		return nil, errors.New("OCR grant TTL must be 1 minute to 1 hour")
	}
	retention := c.Retention
	if retention == 0 {
		retention = 7 * 24 * time.Hour
	}
	if retention < ttl || retention > 7*24*time.Hour {
		return nil, errors.New("OCR retention must cover grant TTL and be at most 7 days")
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	supplier := c.SupplierHTTP
	if supplier == nil {
		supplier = base.GetSSRFProtectedHTTPClient()
	}
	if supplier == nil {
		return nil, errors.New("OCR supplier HTTP client unavailable")
	}
	copyClient := *supplier
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	copyClient.Timeout = 60 * time.Second
	fetch := c.FetchHTTP
	if fetch == nil {
		fetch = base.GetSSRFProtectedHTTPClient()
	}
	if fetch == nil {
		return nil, errors.New("OCR protected fetch client unavailable")
	}
	return &Service{db: db, billing: billing, objects: c.Objects, keys: c.GatewayKeys, supplier: paddleocr.Client{HTTP: &copyClient}, fetchClient: fetch, grantSecret: append([]byte(nil), c.GrantSecret...), publicOrigin: origin, grantTTL: ttl, retention: retention, now: now}, nil
}

type JobStatus struct {
	JobID           string `json:"jobId"`
	State           string `json:"state"`
	ExtractProgress struct {
		TotalPages     int `json:"totalPages"`
		ExtractedPages int `json:"extractedPages"`
	} `json:"extractProgress"`
	ResultURL struct {
		JSONURL string `json:"jsonUrl,omitempty"`
	} `json:"resultUrl"`
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (JobStatus, error) {
	input, err := s.validateInput(ctx, req.Input)
	if err != nil {
		return JobStatus{}, err
	}
	targetURL, err := url.Parse(req.Target.JobsURL)
	if err != nil || targetURL.Host == "" || targetURL.User != nil || targetURL.RawQuery != "" || targetURL.Fragment != "" || (targetURL.Scheme != "https" && targetURL.Scheme != "http") || !strings.HasSuffix(targetURL.Path, "/api/v2/ocr/jobs") || req.Target.Model == "" {
		return JobStatus{}, errors.New("invalid authorized PaddleOCR target")
	}
	maxAttempts := min(max(common.RetryTimes+1, 1), 16)
	if req.MaxAttempts > 0 {
		maxAttempts = min(maxAttempts, req.MaxAttempts)
	}
	if req.DisableRetry {
		maxAttempts = 1
	}
	// Resolve eligibility and current credential store before financial admission.
	channel, err := s.channel(ctx, req.Admission.ChannelID, req.Target.ProfileID)
	if err != nil {
		return JobStatus{}, err
	}
	if req.Target.ProfileID != 0 {
		key, keyErr := s.keys.GetBYOKKey(ctx, req.Admission.UserID, req.Target.ProfileID)
		if keyErr != nil {
			return JobStatus{}, keyErr
		}
		if key != "" {
			return JobStatus{}, errors.New("OCR BYOK owned jobs are not supported; platform fallback forbidden")
		}
	}
	j, err := s.billing.CreateIntent(ctx, req.Admission)
	if err != nil {
		return JobStatus{}, err
	}
	execution := model.OCRExecution{JobsURL: req.Target.JobsURL, Model: req.Target.Model, ProfileID: req.Target.ProfileID, InputDigest: input.digest, InputPages: input.pages, MaxAttempts: maxAttempts}
	if err = s.billing.ConfigureOCRSubmission(ctx, j.ID, execution); err != nil {
		return JobStatus{}, err
	}
	current, err := s.billing.Inspect(ctx, j.ID)
	if err != nil {
		return JobStatus{}, err
	}
	j = current.Job
	if j.SubmissionState == "submitted" || j.SubmissionState == "uncertain" || j.SubmissionState == "submitting" || j.Decision != "" {
		return s.project(ctx, j)
	}
	if j.SubmissionState == "rejected" && !j.RetryRefusal {
		j, err = s.billing.Finish(ctx, j.ID, "failure")
		if err != nil {
			return JobStatus{}, err
		}
		return s.project(ctx, j)
	}
	wire := paddleocr.Input{Model: execution.Model, Filename: input.filename, File: input.data, OptionalPayload: input.optional}
	if input.urlMode {
		r, readErr := s.billing.OCRInput(ctx, j.ID)
		if errors.Is(readErr, gorm.ErrRecordNotFound) {
			r, err = s.storeResource(ctx, j.ID, "input", input.contentType, input.data, s.now().Add(s.retention).Unix())
			if err != nil {
				return JobStatus{}, err
			}
			if err = s.billing.PublishOCRInput(ctx, r); err != nil {
				// Publication may have committed despite an acknowledgement error.
				// Keep the object until scoped retention can establish its owner.
				r, err = s.billing.OCRInput(ctx, j.ID)
				if err != nil {
					return JobStatus{}, err
				}
			}
		} else if readErr != nil {
			return JobStatus{}, readErr
		}
		wire.FileURL, err = s.resourceURL(ctx, r)
		if err != nil {
			return JobStatus{}, err
		}
		wire.File = nil
	}
	j, err = s.billing.Reserve(ctx, j.ID)
	if err != nil {
		return JobStatus{}, err
	}
	for {
		if err = ctx.Err(); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, _ = s.billing.AbandonOCRSubmission(cleanupCtx, j.ID)
			cancel()
			return JobStatus{}, err
		}
		snapshot, err := s.billing.Inspect(ctx, j.ID)
		if err != nil {
			return JobStatus{}, err
		}
		j = snapshot.Job
		if j.SubmissionState != "unclaimed" && !(j.SubmissionState == "rejected" && j.RetryRefusal) {
			return s.project(ctx, j)
		}
		var attempts []model.OCRSubmissionAttempt
		if j.Attempts != "" {
			if err = common.UnmarshalJsonStr(j.Attempts, &attempts); err != nil {
				return JobStatus{}, err
			}
		}
		excluded := map[string]bool{}
		for _, attempt := range attempts {
			excluded[attempt.MemberID] = true
			excluded[attempt.HealthMemberID] = true
		}
		channel, err = s.channel(ctx, j.ChannelID, execution.ProfileID)
		if err != nil {
			return JobStatus{}, err
		}
		selection, key, owner, err := s.selectAccount(ctx, channel, execution.ProfileID, j.Model, excluded)
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, finishErr := s.billing.AbandonOCRSubmission(cleanupCtx, j.ID)
			cancel()
			if finishErr != nil {
				return JobStatus{}, errors.Join(err, finishErr)
			}
			return JobStatus{}, err
		}
		claim, err := s.billing.ClaimSubmission(ctx, j.ID, selection, owner)
		if err != nil {
			if errors.Is(err, model.ErrOCRConflict) {
				current, readErr := s.billing.Inspect(ctx, j.ID)
				if readErr == nil && (current.Job.SubmissionState == "submitting" || current.Job.SubmissionState == "submitted" || current.Job.SubmissionState == "uncertain") {
					return s.project(ctx, current.Job)
				}
			}
			return JobStatus{}, err
		}
		// Claim is the last durable gate. No automatic retry can cross an unknown send.
		id, response, sendErr := s.supplier.Submit(ctx, execution.JobsURL, key, wire)
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		healthErr := s.observe(persistCtx, selection, j.Model, response, sendErr)
		if sendErr == nil {
			err = s.billing.RecordSubmission(persistCtx, claim, id, false)
			cancel()
			if err != nil {
				return JobStatus{}, err
			}
			if healthErr != nil {
				return JobStatus{}, healthErr
			}
			latest, err := s.billing.Inspect(ctx, j.ID)
			if err != nil {
				return JobStatus{}, err
			}
			return s.project(ctx, latest.Job)
		}
		if response.Refused() {
			retry := !req.DisableRetry && !response.RetryVeto() && ctx.Err() == nil && healthErr == nil && j.SubmitAttempts+1 < j.MaxAttempts
			err = s.billing.RecordOCRRefusal(persistCtx, claim, response.Status, retry)
			if err == nil && !retry {
				_, err = s.billing.Finish(persistCtx, j.ID, "failure")
			}
			cancel()
			if err != nil {
				return JobStatus{}, err
			}
			if !retry {
				return JobStatus{}, sendErr
			}
			continue
		}
		err = s.billing.RecordSubmission(persistCtx, claim, "", true)
		cancel()
		if err != nil {
			return JobStatus{}, err
		}
		return JobStatus{}, sendErr
	}
}
func (s *Service) channel(ctx context.Context, id, profileID int) (*model.Channel, error) {
	var ch model.Channel
	if err := s.db.WithContext(ctx).First(&ch, id).Error; err != nil {
		return nil, err
	}
	if ch.Status != common.ChannelStatusEnabled {
		return nil, errors.New("OCR channel unavailable")
	}
	if profileID == 0 && ch.Type != constant.ChannelTypePaddleOCR {
		return nil, errors.New("OCR requires explicit PaddleOCR channel")
	}
	if profileID != 0 {
		if ch.Type != constant.ChannelTypeGateway || s.keys == nil {
			return nil, errors.New("OCR Gateway credential resolver unavailable")
		}
		var profile model.ProviderProfile
		if err := s.db.WithContext(ctx).Where("id = ? AND status = ?", profileID, "active").First(&profile).Error; err != nil {
			return nil, err
		}
	}
	return &ch, nil
}
func (s *Service) selectAccount(ctx context.Context, ch *model.Channel, profileID int, modelName string, excluded map[string]bool) (model.UpstreamAccountSelection, string, model.UpstreamAccountRef, error) {
	var zero model.UpstreamAccountSelection
	scope := model.UpstreamAccountScope{Operation: OCROperation, Model: modelName}
	if profileID != 0 {
		refs, keys, owners, err := s.keys.PlatformAccounts(ctx, profileID, ch.Id)
		if err != nil {
			return zero, "", model.UpstreamAccountRef{}, err
		}
		for _, ref := range refs {
			if excluded[owners[ref.KeyIndex].MemberID] {
				excluded[ref.MemberID] = true
			}
		}
		selected, err := model.SelectUpstreamAccount(ctx, s.db, refs, scope, excluded, time.Now())
		return selected, keys[selected.KeyIndex], owners[selected.KeyIndex], err
	}
	refs, keys, err := model.ChannelAccountRefs(ctx, s.db, ch)
	if err != nil {
		return zero, "", model.UpstreamAccountRef{}, err
	}
	selected, err := model.SelectUpstreamAccount(ctx, s.db, refs, scope, excluded, time.Now())
	if err != nil {
		return selected, "", model.UpstreamAccountRef{}, err
	}
	return selected, keys[selected.KeyIndex], selected.Ref, nil
}
func (s *Service) observe(ctx context.Context, selection model.UpstreamAccountSelection, modelName string, response paddleocr.Response, sendErr error) error {
	scope := model.UpstreamAccountScope{Operation: OCROperation, Model: modelName}
	now := time.Now()
	if sendErr == nil {
		return model.RecordUpstreamAccountSuccess(ctx, s.db, selection, scope, now)
	}
	decision := accountpool.ClassifyFailure(accountpool.FailureInput{StatusCode: response.Status, RetryAfter: response.Header.Get("Retry-After")}, now)
	if decision.Failure.Kind != "" {
		return model.RecordUpstreamAccountFailure(ctx, s.db, selection, scope, decision.Failure, now)
	}
	return nil
}
func (s *Service) Status(ctx context.Context, id string, userID, tokenID int) (JobStatus, error) {
	snapshot, err := s.billing.Inspect(ctx, id)
	if err != nil {
		return JobStatus{}, err
	}
	j := snapshot.Job
	if j.UserID != userID || j.TokenID != tokenID {
		return JobStatus{}, model.ErrOCRConflict
	}
	if j.SubmissionState != "submitted" {
		return s.project(ctx, j)
	}
	if j.ProviderState != "done" && j.ProviderState != "failed" {
		var execution model.OCRExecution
		var binding model.OCRAccountBinding
		if err = common.UnmarshalJsonStr(j.Execution, &execution); err != nil {
			return JobStatus{}, err
		}
		if err = common.UnmarshalJsonStr(j.AccountBinding, &binding); err != nil {
			return JobStatus{}, err
		}
		ch, err := s.channel(ctx, j.ChannelID, execution.ProfileID)
		if err != nil {
			return JobStatus{}, err
		}
		owner := binding.Ref
		if binding.Owner != nil {
			owner = *binding.Owner
		}
		var key string
		if execution.ProfileID != 0 {
			key, err = s.keys.ResolveAccountKey(ctx, owner)
		} else {
			key, err = model.ResolveTaskAccountKey(ctx, s.db, ch, owner)
		}
		if err != nil {
			return JobStatus{}, err
		}
		status, _, err := s.supplier.Status(ctx, execution.JobsURL, key, j.ProviderJobID)
		if err != nil {
			return JobStatus{}, err
		}
		if status.JobID != "" && status.JobID != j.ProviderJobID {
			return JobStatus{}, errors.New("OCR supplier job identity mismatch")
		}
		state := status.State
		// Documented submitted/queued states are projected as pending; never terminal.
		if state == "queued" || state == "submitted" {
			state = "pending"
		}
		if err = s.billing.RecordOCRProgress(ctx, j.ID, j.ClaimID, state, status.ResultURL.JSONURL, status.ExtractProgress.TotalPages, status.ExtractProgress.ExtractedPages); err != nil {
			return JobStatus{}, err
		}
		snapshot, err = s.billing.Inspect(ctx, id)
		if err != nil {
			return JobStatus{}, err
		}
		j = snapshot.Job
	}
	if j.ProviderState == "done" {
		// Supplier success is chargeable even when result delivery fails. Persist the
		// decision before download; a restart can retry delivery without another debit.
		if _, err = s.billing.Finish(ctx, j.ID, "success"); err != nil {
			return JobStatus{}, err
		}
		if j.ResultResourceID == "" {
			if err = s.preserveResults(ctx, j); err != nil {
				return JobStatus{}, err
			}
		}
	} else if j.ProviderState == "failed" {
		if _, err = s.billing.Finish(ctx, j.ID, "failure"); err != nil {
			return JobStatus{}, err
		}
	}
	snapshot, err = s.billing.Inspect(ctx, id)
	if err != nil {
		return JobStatus{}, err
	}
	return s.project(ctx, snapshot.Job)
}
func (s *Service) project(ctx context.Context, j model.OCRJob) (JobStatus, error) {
	if (j.Decision == "failure" || j.Decision == "cancel") && j.BillingState != "refunded" {
		finished, err := s.billing.Finish(ctx, j.ID, j.Decision)
		if err != nil {
			return JobStatus{}, err
		}
		j = finished
	}
	out := JobStatus{JobID: j.ID, State: "pending"}
	out.ExtractProgress.TotalPages = j.TotalPages
	out.ExtractProgress.ExtractedPages = j.ExtractedPages
	if j.ProviderState == "running" {
		out.State = "running"
	}
	if j.Decision == "failure" || j.Decision == "cancel" {
		out.State = "failed"
	}
	if j.ResultResourceID != "" {
		r, err := s.billing.OCRResource(ctx, j.ID, j.ResultResourceID)
		if err != nil {
			return out, err
		}
		out.ResultURL.JSONURL, err = s.resourceURL(ctx, r)
		if err != nil {
			return out, err
		}
		out.State = "done"
	}
	return out, nil
}
