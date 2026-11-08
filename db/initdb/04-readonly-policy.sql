

-- Everything the readonly role may read. Anything not granted here is denied,
-- including tables added later. Safe to re-run after editing:
--   docker compose exec -T postgres psql -U postgres -d pagila -v ON_ERROR_STOP=1 < db/initdb/04-readonly-policy.sql

BEGIN;

ALTER ROLE readonly SET temp_file_limit = '100MB';

-- Owned by postgres, so it reads customer and address with the owner's rights:
-- readonly gets these masked columns without any grant on the raw tables.
CREATE OR REPLACE VIEW customer_safe AS
SELECT c.customer_id, c.store_id, c.first_name, c.last_name,
    left(c.email, 1) || '***@' || split_part(c.email, '@', 2) AS email,
    repeat('*', greatest(length(a.phone) - 4, 0)) || right(a.phone, 4) AS phone,
    a.city_id, c.active, c.create_date
FROM customer c
JOIN address a USING (address_id);

-- Not granted: customer, address, staff (emails, phones, street addresses, password hashes)
-- and the views built on them: customer_list, staff_list, sales_by_store.
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM readonly;
GRANT SELECT ON
    actor, category, city, country, film, film_actor, film_category, film_embedding,
    inventory, language, payment, rental, store,
    actor_info, film_list, nicer_but_slower_film_list, rental_by_category, sales_by_film_category,
    customer_safe
TO readonly;
COMMIT;
