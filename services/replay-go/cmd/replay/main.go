package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	loadDotEnv()

	var cluster string
	var start int64
	var end int64
	flag.StringVar(&cluster, "cluster", "mainnet-beta", "Solana cluster label")
	flag.Int64Var(&start, "start", 0, "start slot (inclusive)")
	flag.Int64Var(&end, "end", 0, "end slot (inclusive)")
	flag.Parse()
	if err := validateSlotRange(start, end); err != nil {
		log.Fatalf("%v", err)
	}

	pgDSN, err := requiredPGDSN()
	if err != nil {
		log.Fatalf("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("pg connect: %v", err)
	}
	defer pool.Close()

	jobID := uuid.New()
	_, err = pool.Exec(ctx,
		"INSERT INTO replay_jobs(job_id, cluster, slot_start, slot_end, status) VALUES($1,$2,$3,$4,'queued')",
		jobID, cluster, start, end,
	)
	if err != nil {
		log.Fatalf("insert job: %v", err)
	}

	fmt.Printf("enqueued replay job %s (%s %d..%d)\n", jobID, cluster, start, end)
	fmt.Println("next: implement a worker that fetches blocks via RPC/archive and re-emits deterministic events into helios.raw_tx")
}

func validateSlotRange(start, end int64) error {
	if start <= 0 || end <= 0 || end < start {
		return fmt.Errorf("invalid slot range: start=%d end=%d", start, end)
	}
	return nil
}

func requiredPGDSN() (string, error) {
	v := strings.TrimSpace(os.Getenv("HELIOS_PG_DSN"))
	if v == "" {
		return "", errors.New("HELIOS_PG_DSN is required (use a Neon connection string, typically with sslmode=require)")
	}
	return v, nil
}

func loadDotEnv() {
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../../.env")
}
