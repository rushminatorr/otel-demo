package main

import (
	"context"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/streadway/amqp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpgrpc"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/basic"
	processor "go.opentelemetry.io/otel/sdk/metric/processor/basic"
	"go.opentelemetry.io/otel/sdk/metric/selector/simple"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
)

var (
	ch *amqp.Channel
	// valueRecorder
)

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
	}
}

func main() {
	ctx := context.Background()
	driver := otlpgrpc.NewDriver(
		otlpgrpc.WithInsecure(),
		otlpgrpc.WithEndpoint("localhost:55680"),
		otlpgrpc.WithDialOption(grpc.WithBlock()), // useful for testing
	)
	exporter, err := otlp.NewExporter(ctx, driver)

	if err != nil {
		log.Fatalf("failed to initialize stdout export pipeline: %v", err)
	}

	bsp := sdktrace.NewBatchSpanProcessor(exporter)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(bsp))

	// Handle this error in a sensible manner where possible
	defer func() { _ = tp.Shutdown(ctx) }()

	pusher := controller.New(
		processor.New(
			simple.NewWithExactDistribution(),
			exporter,
		),
		controller.WithPusher(exporter),
		controller.WithCollectPeriod(10*time.Second),
	)

	err = pusher.Start(ctx)
	if err != nil {
		log.Fatalf("failed to initialize metric controller: %v", err)
	}

	// Handle this error in a sensible manner where possible
	defer func() { _ = pusher.Stop(ctx) }()

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(pusher.MeterProvider())
	propagator := propagation.NewCompositeTextMapPropagator(propagation.Baggage{}, propagation.TraceContext{})
	otel.SetTextMapPropagator(propagator)

	lemonsKey := label.Key("lemons")
	commonLabels := []label.KeyValue{lemonsKey.Int(10), label.String("A", "1"), label.String("B", "2"), label.String("C", "3")}

	meter := otel.Meter("rush")
	observerCallback := func(_ context.Context, result metric.Float64ObserverResult) {
		result.Observe(3, commonLabels...)
	}
	_ = metric.Must(meter).NewFloat64ValueObserver("observableValue", observerCallback,
		metric.WithDescription("A ValueObserver set to 3.0"),
	)

	////////////// Rabbit Setup //////////////////////////////////////////
	// conn, err := amqp.Dial("amqp://guest:guest@rabbitmq:5672/")
	conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	failOnError(err, "Failed to connect to RabbitMQ")
	defer conn.Close()

	ch, err = conn.Channel()
	failOnError(err, "Failed to open a channel")
	defer ch.Close()

	err = ch.ExchangeDeclare(
		"colours", // name
		"direct",  // type
		true,      // durable
		false,     // auto-deleted
		false,     // internal
		false,     // no-wait
		nil,       // arguments
	)
	failOnError(err, "Failed to declare an exchange")
	//////////////////////////////////////////////////////////////////////

	setupRoutes()
	log.Print("Routes Setup, starting server...")
	err = http.ListenAndServe(":3333", nil)
	if err != nil {
		panic(err)
	}

}

func setupRoutes() {
	http.HandleFunc("/blue", blue)
}

func blue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ctx = baggage.ContextWithValues(ctx,
		label.String("rush", "test"),
	)
	tracer := otel.Tracer("rush")
	meter := otel.Meter("rush")
	var span trace.Span
	ctx, span = tracer.Start(ctx, "sending msg")
	span.SetAttributes(label.String("type", "blue"))
	defer span.End()

	valueRecorder := metric.Must(meter).NewFloat64ValueRecorder("sendingBlue")

	meter.RecordBatch(
		// Note: call-site variables added as context Entries:
		baggage.ContextWithValues(ctx, label.String("colour", "blue")),
		[]label.KeyValue{label.String("more", "labels")},
		valueRecorder.Measurement(7.0),
	)

	reqBody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Fatal(err)
	}

	publishMessage(ctx, "blue", string(reqBody), ch)
}

func publishMessage(ctx context.Context, routingKey string, body string, ch *amqp.Channel) {
	// span := trace.SpanFromContext(ctx)
	tracer := otel.Tracer("rush")
	var span trace.Span
	ctx, span = tracer.Start(ctx, "publish mesg...")
	defer span.End()
	span.AddEvent("rabbit message", trace.WithAttributes(label.String("routingKey", routingKey)))

	meter := otel.Meter("rush")

	valueRecorder := metric.Must(meter).NewFloat64ValueRecorder("bunnyblue")
	boundRecorder := valueRecorder.Bind(label.String("bound", "recorder"))
	defer boundRecorder.Unbind()
	boundRecorder.Record(ctx, 7.3)

	err := ch.Publish(
		"colours",  // exchange
		routingKey, // routing key
		false,      // mandatory
		false,      // immediate
		amqp.Publishing{
			// Headers:       otel. .InjectContext(ctx, make(amqp.Table)),
			CorrelationId: "abc",
			ContentType:   "text/plain",
			Body:          []byte(body),
		})
	failOnError(err, "Failed to publish a message")

	log.Printf("Sent Message: %s", body)
}
