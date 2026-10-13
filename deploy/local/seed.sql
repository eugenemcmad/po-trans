-- Local environments only (demo-setup, integration test helpers). Never applied in production.
INSERT INTO accounts (id, balance)
SELECT 'acc_' || n, 1000 FROM generate_series(1, 10) AS n;
