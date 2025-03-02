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
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{}))

	if err := run(context.Background(), logger); err != nil {
		logger.Error("startup finished", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
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

	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		requestID, err := uuid.NewRandom()
		if err != nil {
			logger.Error("failed generating request id")
			requestID = uuid.UUID([16]byte{})
		}

		logger := logger.With(
			"request.path", r.URL.Path,
			"request.id", requestID,
		)

		logger.Info("received request")

		requestReceivedAt := time.Now()
		defer func() {
			logger.Info("finished handling request", "duration_ms", time.Since(requestReceivedAt).Milliseconds())
		}()

		if !discordgo.VerifyInteraction(r, publicKeyBytes) {
			logger.Error("failed verifying interaction")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		defer r.Body.Close()
		var interaction discordgo.Interaction
		if err := json.NewDecoder(r.Body).Decode(&interaction); err != nil {
			logger.Error("invalid interaction payload", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if interaction.Type == discordgo.InteractionPing {
			w.WriteHeader(http.StatusOK)
			resp := discordgo.InteractionResponse{
				Type: discordgo.InteractionResponsePong,
			}

			if err := json.NewEncoder(w).Encode(resp); err != nil {
				logger.Error("failed sending ping response", "error", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			return
		}

		if interaction.Type != discordgo.InteractionApplicationCommand {
			logger.Error("unsupported interaction type", "type", interaction.Type)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		applicationCommandData := interaction.ApplicationCommandData()

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

		lcResp, err := leetcodeClient.RandomQuestion(difficulty)
		if err != nil {
			logger.Error("failed to retrieve leetcode question", "error", err)
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
			logger.Error("failed responding to interaction")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		logger.Info("responded to interaction", "leetcode", lcResp.Data.RandomQuestion.TitleSlug, "difficulty", difficulty)

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
