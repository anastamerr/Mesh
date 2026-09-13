ALTER TABLE relay_tickets
  ADD COLUMN route text NOT NULL DEFAULT 'storage',
  ADD COLUMN workload_id uuid REFERENCES workloads(id),
  ADD CONSTRAINT relay_ticket_route CHECK (
    (route = 'storage' AND workload_id IS NULL) OR
    (workload_id IS NOT NULL AND route = 'app-' || workload_id::text)
  );

CREATE INDEX relay_tickets_route_idx ON relay_tickets(node_id, route, role, expires_at);
