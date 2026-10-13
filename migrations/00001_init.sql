-- Schema of the transfer service. Applied in every environment (production, demo, tests).

CREATE TABLE accounts (
    id      TEXT   PRIMARY KEY,
    balance BIGINT NOT NULL CHECK (balance >= 0)
);

-- No foreign keys to accounts: rejected transfers for unknown accounts are stored too.
CREATE TABLE transfers (
    id              UUID        PRIMARY KEY,
    idempotency_key TEXT        NOT NULL UNIQUE,
    from_account_id TEXT        NOT NULL,
    to_account_id   TEXT        NOT NULL,
    amount          BIGINT      NOT NULL CHECK (amount > 0),
    status          TEXT        NOT NULL CHECK (status IN ('completed', 'rejected')),
    reason          TEXT        NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (status = 'completed' AND reason IS NULL) OR
        (status = 'rejected'  AND reason IS NOT NULL AND reason <> '')
    )
);
