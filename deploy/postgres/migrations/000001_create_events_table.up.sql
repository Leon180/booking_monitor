CREATE TABLE IF NOT EXISTS events (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    total_tickets INT NOT NULL,
    available_tickets INT NOT NULL,
    version INT DEFAULT 0
);

INSERT INTO events (name, total_tickets, available_tickets) VALUES ('Jay Chou Concert', 100, 100);
