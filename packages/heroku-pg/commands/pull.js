'use strict'

const cli = require('heroku-cli-util')
const co = require('co')
const debug = require('debug')('push')
const psql = require('../lib/psql')
const env = require('process').env

function parseURL (db) {
  const url = require('url')
  db = url.parse(db.match(/:\/\//) ? db : `postgres:///${db}`)
  let [user, password] = db.auth ? db.auth.split(':') : []
  db.user = user
  db.password = password
  db.database = db.path ? db.path.split('/', 2)[1] : null
  db.host = db.hostname || 'localhost'
  db.port = db.port || env.PGPORT || 5432
  return db
}

function exec (cmd, opts = {}) {
  const {execSync} = require('child_process')
  debug(cmd)
  opts = Object.assign({}, opts, {stdio: 'inherit'})
  try {
    return execSync(cmd, opts)
  } catch (err) {
    if (err.status) process.exit(err.status)
    throw err
  }
}

const prepare = co.wrap(function * (target) {
  if (target.host === 'localhost') {
    exec(`createdb ${connstring(target, true)}`)
  } else {
    let result = yield psql.exec(target, 'SELECT COUNT(*) = 0 AS empty FROM pg_stat_user_tables')
    if (!result.startsWith(' empty \n-------\n t')) throw new Error(`Remote database is not empty. Please create a new database or use ${cli.color.cmd('heroku pg:reset')}`)
  }
})

function connstring (uri, skipDFlag) {
  let user = uri.user ? `-U ${uri.user}` : ''
  return `${user} -h ${uri.host} -p ${uri.port || 5432} ${skipDFlag ? '' : '-d'} ${uri.database}`
}

const verifyExtensionsMatch = co.wrap(function * (source, target) {
  // It's pretty common for local DBs to not have extensions available that
  // are used by the remote app, so take the final precaution of warning if
  // the extensions available in the local database don't match. We don't
  // report it if the difference is solely in the version of an extension
  // used, though.
  let sql = 'SELECT extname FROM pg_extension ORDER BY extname;'
  let extensions = yield {
    target: psql.exec(target, sql),
    source: psql.exec(source, sql)
  }
  // TODO: it shouldn't matter if the target has *more* extensions than the source
  if (extensions.target !== extensions.source) {
    cli.warn(`WARNING: Extensions in newly created target database differ from existing source database.
Target extensions:
${extensions.target}
Source extensions:
${extensions.source}
HINT: You should review output to ensure that any errors
ignored are acceptable - entire tables may have been missed, where a dependency
could not be resolved. You may need to to install a postgresql-contrib package
and retry.`)
  }
})

const run = co.wrap(function * (source, target) {
  yield prepare(target)
  let password = p => p ? ` PGPASSWORD="${p}"` : ''
  let dump = `env${password(source.password)} PGSSLMODE=prefer pg_dump --verbose -F c -Z 0 ${connstring(source, true)}`
  let restore = `env${password(target.password)} pg_restore --verbose --no-acl --no-owner ${connstring(target)}`
  exec(`${dump} | ${restore}`)
  yield verifyExtensionsMatch(source, target)
})

function * push (context, heroku) {
  const fetcher = require('../lib/fetcher')(heroku)
  const {app, args} = context
  const source = parseURL(args.source)
  const target = yield fetcher.database(app, args.target)
  cli.log(`heroku-cli: Pushing ${cli.color.cyan(args.source)} ---> ${cli.color.addon(target.attachment.addon.name)}`)
  yield run(source, target)
  cli.log('heroku-cli: Pushing complete.')
}

function * pull (context, heroku) {
  const fetcher = require('../lib/fetcher')(heroku)
  const {app, args} = context
  const source = yield fetcher.database(app, args.source)
  const target = parseURL(args.target)
  cli.log(`heroku-cli: Pulling ${cli.color.addon(source.attachment.addon.name)} ---> ${cli.color.cyan(args.target)}`)
  yield run(source, target)
  cli.log('heroku-cli: Pulling complete.')
}

let cmd = {
  topic: 'pg',
  needsApp: true,
  needsAuth: true,
  args: [{name: 'source'}, {name: 'target'}]
}

module.exports = [
  Object.assign({
    command: 'push',
    description: 'push local or remote into Heroku database',
    help: `Push from SOURCE into TARGET. TARGET must not already exist.

To empty a Heroku database for import run 'heroku pg:reset'

SOURCE must be either the name of a database existing on your localhost or the
fully qualified URL of a remote database.

Examples:

  # push mylocaldb into a Heroku DB named postgresql-swimmingly-100
  $ heroku pg:push mylocaldb postgresql-swimmingly-100

  # push remote DB at postgres://myhost/mydb into a Heroku DB named postgresql-swimmingly-100
  $ heroku pg:push postgres://myhost/mydb postgresql-swimmingly-100
`,
    run: cli.command({preauth: true}, co.wrap(push))
  }, cmd),
  Object.assign({
    command: 'pull',
    description: 'pull Heroku database into local or remote database',
    help: `Pull from SOURCE into TARGET. TARGET must not already exist.

To delete a local database run \`dropdb TARGET\`

TARGET will be created locally if it's a database name or remotely if it's a fully qualified URL.

Examples:

  # pull Heroku DB named postgresql-swimmingly-100 into local DB mylocaldb
  $ heroku pg:push postgresql-swimmingly-100 mylocaldb

  # pull Heroku DB named postgresql-swimmingly-100 into remote DB at postgres://myhost/mydb
  $ heroku pg:push postgresql-swimmingly-100 postgres://myhost/mydb
`,
    run: cli.command({preauth: true}, co.wrap(pull))
  }, cmd)
]
