package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type OrderStats struct {
	TotalOrders     int64          `json:"total_orders"`
	TotalRevenue    float64        `json:"total_revenue"`
	AvgOrderValue   float64        `json:"avg_order_value"`
	UniqueCustomers int64          `json:"unique_customers"`
	TopCategories   []CategoryStat `json:"top_categories"`
}

type CategoryStat struct {
	Category string  `json:"category"`
	Revenue  float64 `json:"revenue"`
	Orders   int64   `json:"orders"`
}

type Repository struct {
	db *pgxpool.Pool
}

func New(dsn string) (*Repository, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 2

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Repository{db: pool}, nil
}

func (r *Repository) Close() {
	r.db.Close()
}

// ComputeStats runs an expensive aggregation over the orders table.
// On a 500k-row dataset this typically takes 100–500ms.
func (r *Repository) ComputeStats(ctx context.Context) (*OrderStats, error) {
	stats := &OrderStats{}

	row := r.db.QueryRow(ctx, `
		SELECT
			COUNT(*)                         AS total_orders,
			COALESCE(SUM(amount), 0)         AS total_revenue,
			COALESCE(AVG(amount), 0)         AS avg_order_value,
			COUNT(DISTINCT customer_id)      AS unique_customers
		FROM orders
	`)
	if err := row.Scan(&stats.TotalOrders, &stats.TotalRevenue, &stats.AvgOrderValue, &stats.UniqueCustomers); err != nil {
		return nil, fmt.Errorf("scan aggregate: %w", err)
	}

	rows, err := r.db.Query(ctx, `
		SELECT
			p.category,
			ROUND(SUM(o.amount)::numeric, 2) AS revenue,
			COUNT(*)                         AS orders
		FROM orders o
		JOIN products p ON o.product_id = p.id
		GROUP BY p.category
		ORDER BY revenue DESC
		LIMIT 5
	`)
	if err != nil {
		return nil, fmt.Errorf("query categories: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cs CategoryStat
		if err := rows.Scan(&cs.Category, &cs.Revenue, &cs.Orders); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		stats.TopCategories = append(stats.TopCategories, cs)
	}
	return stats, rows.Err()
}
