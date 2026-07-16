const fs = require('fs');
const path = require('path');
const { Client } = require('pg');

// ---- load .env (no secrets printed) ----
const env = {};
for (const line of fs.readFileSync('.env', 'utf8').split('\n')) {
  const m = line.match(/^([A-Z_]+)="?(.*?)"?$/);
  if (m) env[m[1]] = m[2];
}
const ref = env.SUPABASE_URL.replace(/^https?:\/\//, '').replace(/\/$/, ''); // e.g. rvbuzyljrwsxfxijotdf.supabase.co
const dbHost = 'db.' + ref;
const pwd = encodeURIComponent(env.DATABASE_PASSWORD);
const connStr = `postgresql://postgres:${pwd}@${dbHost}:5432/postgres`;

const client = new Client({ connectionString: connStr, connectionTimeoutMillis: 15000 });

(async () => {
  try {
    await client.connect();
    console.log('[ok] connected to postgres', dbHost);

    // 1. current constraint state
    const constr = await client.query(`
      SELECT conname, contype
      FROM pg_constraint
      WHERE conrelid = 'channel_assignments'::regclass
        AND contype IN ('p','u')`);
    console.log('[info] channel_assignments existing PK/UNIQUE constraints:',
      constr.rows.length ? constr.rows.map(r => `${r.contype}:${r.conname}`).join(', ') : 'NONE');

    // 2. find duplicate (username, site) groups
    const dups = await client.query(`
      SELECT username, site, count(*) AS n
      FROM channel_assignments
      GROUP BY username, site
      HAVING count(*) > 1
      ORDER BY n DESC, username`);
    console.log(`\n[scan] duplicate (username,site) groups in channel_assignments: ${dups.rows.length}`);
    let toDelete = 0;
    for (const d of dups.rows) {
      const extra = d.n - 1;
      toDelete += extra;
      console.log(`   ${d.username} / ${d.site} -> ${d.n} rows (will keep 1, delete ${extra})`);
    }

    // 3. delete extra copies, keeping the row with the greatest ctid
    if (toDelete > 0) {
      const res = await client.query(`
        DELETE FROM channel_assignments a
        USING channel_assignments b
        WHERE a.username = b.username
          AND a.site = b.site
          AND a.ctid < b.ctid`);
      console.log(`\n[clean] deleted ${res.rowCount} duplicate row(s), kept one of each channel`);
    } else {
      console.log('\n[clean] no duplicate rows to delete');
    }

    // 4. add PRIMARY KEY (username, site) if missing
    await client.query(`
      DO $$
      BEGIN
        IF NOT EXISTS (
          SELECT 1 FROM pg_constraint
          WHERE conrelid = 'channel_assignments'::regclass
            AND contype = 'p'
        ) THEN
          ALTER TABLE channel_assignments ADD PRIMARY KEY (username, site);
          RAISE NOTICE 'PK added';
        ELSE
          RAISE NOTICE 'PK already present';
        END IF;
      END $$;`);

    // 5. ensure channels.username is unique (mirror migrate.sql intent)
    await client.query(`
      DO $$
      BEGIN
        IF NOT EXISTS (
          SELECT 1 FROM pg_constraint
          WHERE conrelid = 'channels'::regclass
            AND contype IN ('p','u')
            AND array_to_string(a.attname,',') LIKE '%username%'
          FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum = ANY(c.conkey)
          WHERE c.conrelid = 'channels'::regclass AND c.contype IN ('p','u')
        ) THEN
          BEGIN
            ALTER TABLE channels ADD CONSTRAINT channels_username_key UNIQUE (username);
            RAISE NOTICE 'channels.username unique added';
          EXCEPTION WHEN others THEN
            RAISE NOTICE 'channels.username unique add failed: %', SQLERRM;
          END;
        ELSE
          RAISE NOTICE 'channels.username unique already present';
        END IF;
      END $$;`);

    // 6. re-verify
    const afterCA = await client.query(`
      SELECT count(*) AS dups FROM (
        SELECT 1 FROM channel_assignments GROUP BY username, site HAVING count(*) > 1
      ) t`);
    const afterCh = await client.query(`
      SELECT count(*) AS dups FROM (
        SELECT 1 FROM channels GROUP BY username HAVING count(*) > 1
      ) t`);
    console.log(`\n[verify] remaining duplicate groups -> channel_assignments: ${afterCA.rows[0].dups}, channels: ${afterCh.rows[0].dups}`);
    const totalCA = await client.query(`SELECT count(*) AS c FROM channel_assignments`);
    const totalCh = await client.query(`SELECT count(*) AS c FROM channels`);
    console.log(`[verify] total rows -> channel_assignments: ${totalCA.rows[0].c}, channels: ${totalCh.rows[0].c}`);
    console.log('\nDONE.');
  } catch (e) {
    console.error('[ERROR]', e.message);
    process.exitCode = 1;
  } finally {
    await client.end();
  }
})();
