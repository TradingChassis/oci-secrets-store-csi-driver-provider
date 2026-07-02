/*
** OCI Secrets Store CSI Driver Provider
**
** Copyright (c) 2022 Oracle America, Inc. and its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */
package metrics

import (
	"context"

	"github.com/rs/zerolog/log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	providerAttr    = attribute.String("provider", "oci-provider")
	serviceNameAttr = attribute.String("service.name", "oci-secrets-store-csi-driver-provider")
	grpcMethodKey   = "grpc_method"
	grpcCodeKey     = "grpc_code"
	grpcMessageKey  = "grpc_message"
)

type reporter struct {
	grpcRequest metric.Float64Histogram
}

// StatsReporter is the interface for reporting metrics
type StatsReporter interface {
	ReportGRPCRequest(ctx context.Context, duration float64, method, code, message string)
}

// NewStatsReporter creates a new StatsReporter
func NewStatsReporter() StatsReporter { //nolint:ireturn //known
	meter := otel.Meter("oci-secrets-store-csi-driver-provider")

	grpcRequest, err := meter.Float64Histogram("grpc_request",
		metric.WithDescription("Distribution of how long it took for the gRPC requests"))
	if err != nil {
		log.Error().Err(err).Msg("failed to create grpc request metric")
	}
	return &reporter{grpcRequest: grpcRequest}
}

// ReportGRPCRequest reports the duration of the gRPC request
// method and code are used to identify the gRPC request
func (r *reporter) ReportGRPCRequest(ctx context.Context, duration float64, method, code, message string) {
	if r.grpcRequest == nil {
		return
	}
	attributes := []attribute.KeyValue{
		serviceNameAttr,
		providerAttr,
		attribute.String(grpcMethodKey, method),
		attribute.String(grpcCodeKey, code),
		attribute.String(grpcMessageKey, message),
	}
	r.grpcRequest.Record(ctx, duration, metric.WithAttributes(attributes...))
}
