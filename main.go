package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	grpcmw "github.com/grpc-ecosystem/go-grpc-middleware"
	grpczap "github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	grpcprom "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/jzelinskie/stringz"
	"github.com/mwitkow/grpc-proxy/proxy"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

var rootCmd = &cobra.Command{
	Use:               "grpcwebproxy",
	Short:             "A proxy that converts grpc-web into grpc.",
	Long:              "A proxy that converts grpc-web into grpc.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return syncViper(cmd, "GRPCWEBPROXY") },
	Run:               rootRun,
}

func syncViper(cmd *cobra.Command, prefix string) error {
	v := viper.New()
	viper.SetEnvPrefix(prefix)

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		suffix := strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		v.BindEnv(f.Name, prefix+"_"+suffix)

		if !f.Changed && v.IsSet(f.Name) {
			val := v.Get(f.Name)
			cmd.Flags().Set(f.Name, fmt.Sprintf("%v", val))
		}
	})

	return nil
}

func main() {
	rootCmd.Flags().String("upstream-addr", "127.0.0.1:50051", "address of the upstream gRPC service")
	rootCmd.Flags().String("upstream-cert-path", "", "local path to the TLS certificate of the upstream gRPC service")
	rootCmd.Flags().String("web-addr", ":80", "address to listen on for grpc-web requests")
	rootCmd.Flags().String("web-key-path", "", "local path to the TLS key of the grpc-web server")
	rootCmd.Flags().String("web-cert-path", "", "local path to the TLS certificate of the grpc-web server")
	rootCmd.Flags().String("web-allowed-origins", "", "CORS allowed origins for grpc-web (comma-separated)")
	rootCmd.Flags().String("metrics-addr", ":9090", "address to listen on for the metrics server")
	rootCmd.Flags().Bool("debug", false, "debug log verbosity")

	rootCmd.Execute()
}

func rootRun(cmd *cobra.Command, args []string) {
	logger, _ := zap.NewProduction()
	if MustGetBool(cmd, "debug") {
		logger, _ = zap.NewDevelopment()
	}
	defer logger.Sync()

	upstream, err := NewUpstreamConnection(MustGetString(cmd, "upstream-addr"), MustGetString(cmd, "upstream-cert-path"))
	if err != nil {
		logger.Fatal("failed to connect to upstream", zap.String("error", err.Error()))
	}

	srv, err := NewGrpcProxyServer(logger, upstream)
	if err != nil {
		logger.Fatal("failed to init grpc server", zap.String("error", err.Error()))
	}

	origins := strings.Split(MustGetString(cmd, "web-allowed-origins"), ",")
	grpcwebsrv, err := NewGrpcWebServer(srv, origins)
	if err != nil {
		logger.Fatal("failed to init grpcweb server", zap.String("error", err.Error()))
	}

	go func() {
		certPath := MustGetString(cmd, "web-cert-path")
		keyPath := MustGetString(cmd, "web-key-path")
		websrv := &http.Server{
			Addr:    MustGetString(cmd, "web-addr"),
			Handler: grpcwebsrv,
		}

		if certPath != "" && keyPath != "" {
			logger.Info(
				"grpc-web server listening over HTTPS",
				zap.String("addr", MustGetString(cmd, "web-addr")),
				zap.String("certPath", certPath),
				zap.String("keyPath", keyPath),
			)
			websrv.ListenAndServeTLS(certPath, keyPath)
		} else {
			logger.Info(
				"grpc-web server listening over HTTP",
				zap.String("addr", MustGetString(cmd, "web-addr")),
			)
			websrv.ListenAndServe()
		}
	}()

	logger.Info("metrics server listening over HTTP", zap.String("addr", MustGetString(cmd, "metrics-addr")))
	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(MustGetString(cmd, "metrics-addr"), nil)
}

func NewGrpcWebServer(srv *grpc.Server, allowedOrigins []string) (*grpcweb.WrappedGrpcServer, error) {
	return grpcweb.WrapServer(srv,
		grpcweb.WithCorsForRegisteredEndpointsOnly(false),
		grpcweb.WithOriginFunc(NewAllowedOriginsFunc(allowedOrigins)),
	), nil
}

func NewGrpcProxyServer(logger *zap.Logger, upstream *grpc.ClientConn) (*grpc.Server, error) {
	grpc.EnableTracing = true
	grpczap.ReplaceGrpcLogger(logger)

	// If the connection header is present in the request from the web client,
	// the actual connection to the backend will not be established.
	// https://github.com/improbable-eng/grpc-web/issues/568
	director := func(ctx context.Context, _ string) (context.Context, *grpc.ClientConn, error) {
		metadataIn, _ := metadata.FromIncomingContext(ctx)
		md := metadataIn.Copy()
		delete(md, "user-agent")
		delete(md, "connection")
		return metadata.NewOutgoingContext(ctx, md), upstream, nil
	}

	return grpc.NewServer(
		grpc.CustomCodec(proxy.Codec()),
		grpc.UnknownServiceHandler(proxy.TransparentHandler(director)),
		grpcmw.WithUnaryServerChain(
			grpczap.UnaryServerInterceptor(logger),
			grpcprom.UnaryServerInterceptor,
		),
		grpcmw.WithStreamServerChain(
			grpczap.StreamServerInterceptor(logger),
			grpcprom.StreamServerInterceptor,
		),
	), nil
}

func NewUpstreamConnection(addr string, certPath string) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption
	if certPath != "" {
		creds, err := credentials.NewClientTLSFromFile(certPath, "")
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
	} else {
		opts = append(opts, grpc.WithInsecure())
	}

	opts = append(opts, grpc.WithCodec(proxy.Codec()))
	return grpc.Dial(addr, opts...)
}

func NewAllowedOriginsFunc(urls []string) func(string) bool {
	if stringz.SliceEqual(urls, []string{""}) {
		return func(string) bool {
			return true
		}
	}

	return func(origin string) bool {
		return stringz.SliceContains(urls, origin)
	}
}

func MustGetBool(cmd *cobra.Command, key string) bool {
	val, err := cmd.Flags().GetBool(key)
	if err != nil {
		panic(fmt.Sprintf("failed to find flag %s: %s", key, err))
	}
	return val
}

func MustGetString(cmd *cobra.Command, key string) string {
	val, err := cmd.Flags().GetString(key)
	if err != nil {
		panic(fmt.Sprintf("failed to find flag %s: %s", key, err))
	}
	return os.ExpandEnv(val)
}
