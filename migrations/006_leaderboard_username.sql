-- Rebuild leaderboard view to expose username.
CREATE OR REPLACE VIEW leaderboard AS
SELECT
    u.id,
    u.name,
    COALESCE(u.username, '') AS username,
    u.avatar_url,
    u.xp,
    u.streak,
    RANK() OVER (ORDER BY u.xp DESC) AS rank
FROM users u
ORDER BY u.xp DESC
LIMIT 100;
