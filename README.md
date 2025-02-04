# grpcwebproxy

This is a fork of [Improbable's grpcwebproxy][grpcwebproxy].

Alterations include:

- Significantly less code
- Manages dependencies with Go modules
- Dropped support rarely used features: websockets, authority header, grpc max-size
- Logs with [Zap] instead of [Logrus]
- Uses [Viper] & [Cobra] for configuration
- Separate server for serving [Prometheus] metrics

[grpcwebproxy]: https://github.com/improbable-eng/grpc-web/tree/master/go/grpcwebproxy
[Zap]: https://github.com/uber-go/zap
[Logrus]: https://github.com/sirupsen/logrus
[Viper]: https://github.com/spf13/viper
[Cobra]: https://github.com/spf13/cobra
[Prometheus]: https://prometheus.io
