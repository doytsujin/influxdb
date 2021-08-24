package transport

import (
	"net/http"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/influxdata/influxdb/v2"
	"github.com/influxdata/influxdb/v2/kit/feature"
	"github.com/influxdata/influxdb/v2/kit/platform"
	"github.com/influxdata/influxdb/v2/kit/platform/errors"
	kithttp "github.com/influxdata/influxdb/v2/kit/transport/http"
	"go.uber.org/zap"
)

const (
	prefixReplications = "/api/v2/replications"
)

var (
	errBadOrg = &errors.Error{
		Code: errors.EInvalid,
		Msg:  "invalid or missing org ID",
	}

	errBadRemoteID = &errors.Error{
		Code: errors.EInvalid,
		Msg:  "invalid remote ID",
	}

	errBadLocalBucketID = &errors.Error{
		Code: errors.EInvalid,
		Msg:  "invalid local bucket ID",
	}

	errBadId = &errors.Error{
		Code: errors.EInvalid,
		Msg:  "replication ID is invalid",
	}
)

type ReplicationHandler struct {
	chi.Router

	log *zap.Logger
	api *kithttp.API

	replicationsService influxdb.ReplicationService
}

func NewReplicationHandler(log *zap.Logger, svc influxdb.ReplicationService) *ReplicationHandler {
	h := &ReplicationHandler{
		log:                 log,
		api:                 kithttp.NewAPI(kithttp.WithLog(log)),
		replicationsService: svc,
	}

	r := chi.NewRouter()
	r.Use(
		middleware.Recoverer,
		middleware.RequestID,
		middleware.RealIP,
		h.mwReplicationsFlag,
	)

	r.Route("/", func(r chi.Router) {
		r.Get("/", h.handleGetReplications)
		r.Post("/", h.handlePostReplication)

		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", h.handleGetReplication)
			r.Patch("/", h.handlePatchReplication)
			r.Delete("/", h.handleDeleteReplication)
			r.Post("/validate", h.handleValidateReplication)
		})
	})

	h.Router = r
	return h
}

func (h *ReplicationHandler) Prefix() string {
	return prefixReplications
}

func (h *ReplicationHandler) mwReplicationsFlag(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flags := feature.FlagsFromContext(r.Context())

		if flagVal, ok := flags[feature.ReplicationStreamBackend().Key()]; !ok || !flagVal.(bool) {
			h.api.Respond(w, r, http.StatusNotFound, nil)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (h *ReplicationHandler) handleGetReplications(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// orgID is required for listing replications.
	orgID := q.Get("orgID")
	o, err := platform.IDFromString(orgID)
	if err != nil {
		h.api.Err(w, r, errBadOrg)
		return
	}

	// name, remoteID, and localBucketID are optional additional filters.
	name := q.Get("name")
	remoteID := q.Get("remoteID")
	localBucketID := q.Get("localBucketID")

	filters := influxdb.ReplicationListFilter{OrgID: *o}
	if name != "" {
		filters.Name = &name
	}
	if remoteID != "" {
		i, err := platform.IDFromString(remoteID)
		if err != nil {
			h.api.Err(w, r, errBadRemoteID)
			return
		}
		filters.RemoteID = i
	}
	if localBucketID != "" {
		i, err := platform.IDFromString(localBucketID)
		if err != nil {
			h.api.Err(w, r, errBadLocalBucketID)
			return
		}
		filters.LocalBucketID = i
	}

	rs, err := h.replicationsService.ListReplications(r.Context(), filters)
	if err != nil {
		h.api.Err(w, r, err)
		return
	}
	h.api.Respond(w, r, http.StatusOK, rs)
}

func (h *ReplicationHandler) handlePostReplication(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	validate := q.Get("validate") == "true"
	req := influxdb.CreateReplicationRequest{MaxQueueSizeBytes: influxdb.DefaultReplicationMaxQueueSizeBytes}
	if err := h.api.DecodeJSON(r.Body, &req); err != nil {
		h.api.Err(w, r, err)
		return
	}

	if validate {
		if err := h.replicationsService.ValidateNewReplication(ctx, req); err != nil {
			h.api.Err(w, r, err)
			return
		}
		h.api.Respond(w, r, http.StatusNoContent, nil)
		return
	}

	replication, err := h.replicationsService.CreateReplication(ctx, req)
	if err != nil {
		h.api.Err(w, r, err)
		return
	}
	h.api.Respond(w, r, http.StatusCreated, replication)
}

func (h *ReplicationHandler) handleGetReplication(w http.ResponseWriter, r *http.Request) {
	id, err := platform.IDFromString(chi.URLParam(r, "id"))
	if err != nil {
		h.api.Err(w, r, errBadId)
		return
	}

	replication, err := h.replicationsService.GetReplication(r.Context(), *id)
	if err != nil {
		h.api.Err(w, r, err)
		return
	}
	h.api.Respond(w, r, http.StatusOK, replication)
}

func (h *ReplicationHandler) handlePatchReplication(w http.ResponseWriter, r *http.Request) {
	id, err := platform.IDFromString(chi.URLParam(r, "id"))
	if err != nil {
		h.api.Err(w, r, errBadId)
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	validate := q.Get("validate") == "true"
	var req influxdb.UpdateReplicationRequest
	if err := h.api.DecodeJSON(r.Body, &req); err != nil {
		h.api.Err(w, r, err)
		return
	}

	if validate {
		if err := h.replicationsService.ValidateUpdatedReplication(ctx, *id, req); err != nil {
			h.api.Err(w, r, err)
			return
		}
		h.api.Respond(w, r, http.StatusNoContent, nil)
		return
	}

	replication, err := h.replicationsService.UpdateReplication(ctx, *id, req)
	if err != nil {
		h.api.Err(w, r, err)
		return
	}
	h.api.Respond(w, r, http.StatusOK, replication)
}

func (h *ReplicationHandler) handleDeleteReplication(w http.ResponseWriter, r *http.Request) {
	id, err := platform.IDFromString(chi.URLParam(r, "id"))
	if err != nil {
		h.api.Err(w, r, errBadId)
		return
	}

	if err := h.replicationsService.DeleteReplication(r.Context(), *id); err != nil {
		h.api.Err(w, r, err)
		return
	}
	h.api.Respond(w, r, http.StatusNoContent, nil)
}

func (h *ReplicationHandler) handleValidateReplication(w http.ResponseWriter, r *http.Request) {
	id, err := platform.IDFromString(chi.URLParam(r, "id"))
	if err != nil {
		h.api.Err(w, r, errBadId)
		return
	}

	if err := h.replicationsService.ValidateReplication(r.Context(), *id); err != nil {
		h.api.Err(w, r, err)
		return
	}
	h.api.Respond(w, r, http.StatusNoContent, nil)
}