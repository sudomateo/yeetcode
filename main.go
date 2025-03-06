package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
	"github.com/sudomateo/yeetcode/internal/leetcode"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.GetTracerProvider().Tracer(
	"github.com/sudomateo/yeetcode",
	trace.WithSchemaURL(semconv.SchemaURL),
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{}))

	if err := run(context.Background(), logger); err != nil {
		logger.Error("startup finished", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	var exporter sdktrace.SpanExporter

	axiomApiToken := os.Getenv("AXIOM_API_TOKEN")
	if axiomApiToken == "" {
		stdoutExp, err := stdouttrace.New()
		if err != nil {
			return fmt.Errorf("failed initializing stdout exporter: %w", err)
		}
		exporter = stdoutExp
	} else {
		httpExp, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint("api.axiom.co"),
			otlptracehttp.WithHeaders(map[string]string{
				"Authorization":   fmt.Sprintf("Bearer %s", axiomApiToken),
				"X-AXIOM-DATASET": "yeetcode",
			}),
		)
		if err != nil {
			return fmt.Errorf("failed initializing trace exporter: %w", err)
		}
		exporter = httpExp
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("yeetcode"),
		)),
	)

	defer func() {
		_ = tracerProvider.Shutdown(ctx)
	}()

	otel.SetTracerProvider(tracerProvider)

	discordToken := os.Getenv("DISCORD_TOKEN")
	if discordToken == "" {
		return errors.New("DISCORD_TOKEN must be provided")
	}

	discordAppPublicKey := os.Getenv("DISCORD_PUBLIC_KEY")
	if discordAppPublicKey == "" {
		return errors.New("DISCORD_PUBLIC_KEY must be provided")
	}

	publicKeyBytes, err := hex.DecodeString(discordAppPublicKey)
	if err != nil {
		return fmt.Errorf("failed decoding application public key: %w", err)
	}

	discordClient, err := discordgo.New(fmt.Sprintf("Bot %s", discordToken))
	if err != nil {
		return fmt.Errorf("failed creating discord client: %w", err)
	}

	leetcodeClient := leetcode.NewClient()

	mux := http.NewServeMux()

	handleFunc := func(pattern string, handlerFunc func(http.ResponseWriter, *http.Request)) {
		handler := otelhttp.WithRouteTag(pattern, http.HandlerFunc(handlerFunc))
		mux.Handle(pattern, handler)
	}

	handleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "interaction")
		defer span.End()

		requestID, err := uuid.NewRandom()
		if err != nil {
			span.RecordError(err)
			requestID = uuid.UUID([16]byte{})
		}

		span.SetAttributes(
			attribute.String("request.id", requestID.String()),
		)

		if !discordgo.VerifyInteraction(r, publicKeyBytes) {
			span.RecordError(err)
			span.SetStatus(codes.Error, "failed verifying interaction")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		defer r.Body.Close()
		var interaction discordgo.Interaction
		if err := json.NewDecoder(r.Body).Decode(&interaction); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "invalid interaction payload")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if interaction.Type == discordgo.InteractionPing {
			w.WriteHeader(http.StatusOK)
			resp := discordgo.InteractionResponse{
				Type: discordgo.InteractionResponsePong,
			}

			if err := json.NewEncoder(w).Encode(resp); err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "failed sending ping response")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			return
		}

		if interaction.Type != discordgo.InteractionApplicationCommand {
			span.RecordError(err)
			span.SetStatus(codes.Error, "unsupported interaction type")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		applicationCommandData := interaction.ApplicationCommandData()
		lcResp, err := fetchLeetCodeQuestion(ctx, &leetcodeClient, &applicationCommandData)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "failed to retreive leetcode question")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		interactionResp := discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("https://leetcode.com/problems/%s", lcResp.Data.RandomQuestion.TitleSlug),
			},
		}

		if err := discordClient.InteractionRespond(&interaction, &interactionResp); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "failed responding to interaction")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		span.SetAttributes(
			attribute.String("leetcode.title_slug", lcResp.Data.RandomQuestion.TitleSlug),
		)

		w.WriteHeader(http.StatusOK)
		return
	})

	srv := http.Server{
		Addr:    ":3000",
		Handler: mux,
	}

	logger.Info("starting http server", "addr", srv.Addr)

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT)
	defer cancel()

	errCh := make(chan error, 1)

	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down gracefully", "reason", ctx.Err())
		shutdownCtx, shutdownCtxCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCtxCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutting down forcefully", "error", err)
			return srv.Close()
		}
	case err := <-errCh:
		return err
	}

	return nil
}

// This is just here to have a parent/child span relationship for Axiom.
func fetchLeetCodeQuestion(ctx context.Context, leetcodeClient *leetcode.Client, applicationCommandData *discordgo.ApplicationCommandInteractionData) (leetcode.RandomQuestionResponse, error) {
	ctx, span := tracer.Start(ctx, "fetchLeetCodeQuestion")
	defer span.End()

	var difficultyOpt string

	for _, v := range applicationCommandData.Options {
		if v.Name == "difficulty" {
			difficultyOpt = strings.ToUpper(v.StringValue())
			break
		}
	}

	var difficulty leetcode.Difficulty

	switch leetcode.Difficulty(difficultyOpt) {
	case leetcode.DifficultyEasy:
		difficulty = leetcode.DifficultyEasy
	case leetcode.DifficultyMedium:
		difficulty = leetcode.DifficultyMedium
	case leetcode.DifficultyHard:
		difficulty = leetcode.DifficultyHard
	default:
		difficulty = leetcode.RandomDifficulty()
	}

	span.SetAttributes(attribute.String("leetcode.difficulty", string(difficulty)))

	return leetcodeClient.RandomQuestion(difficulty)
}
