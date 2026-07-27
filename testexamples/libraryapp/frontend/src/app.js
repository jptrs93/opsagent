import van from 'vanjs-core'
import { capi } from './capi/index.js'
import './style.css'

const { button, div, form, h1, h2, header, input, label, main, option, p, section, select, span, table, tbody, td, th, thead, tr } = van.tags

const library = van.state({ authors: [], genres: [], books: [] })
const loading = van.state(true)
const error = van.state('')

async function run(action) {
  loading.val = true
  error.val = ''
  try {
    library.val = await action()
  } catch (err) {
    error.val = err instanceof Error ? err.message : String(err)
  } finally {
    loading.val = false
  }
}

function textField(name, placeholder, extra = {}) {
  return label(
    span(name),
    input({ name: name.toLowerCase().replaceAll(' ', '_'), placeholder, required: true, ...extra }),
  )
}

function AuthorForm() {
  return form(
    { onsubmit: async (event) => {
      event.preventDefault()
      const target = event.currentTarget
      const data = new FormData(target)
      await run(() => capi.postV1Author({ name: data.get('name').trim() }))
      if (!error.val) target.reset()
    } },
    textField('Name', 'Octavia E. Butler'),
    button({ type: 'submit' }, 'Add author'),
  )
}

function GenreForm() {
  return form(
    { onsubmit: async (event) => {
      event.preventDefault()
      const target = event.currentTarget
      const data = new FormData(target)
      await run(() => capi.postV1Genre({ name: data.get('name').trim() }))
      if (!error.val) target.reset()
    } },
    textField('Name', 'Speculative fiction'),
    button({ type: 'submit' }, 'Add genre'),
  )
}

function BookForm() {
  return form(
    { class: 'book-form', onsubmit: async (event) => {
      event.preventDefault()
      const target = event.currentTarget
      const data = new FormData(target)
      await run(() => capi.postV1Book({
        title: data.get('title').trim(),
        authorId: Number(data.get('author_id')),
        genreId: Number(data.get('genre_id')),
        publicationYear: Number(data.get('publication_year')),
      }))
      if (!error.val) target.reset()
    } },
    textField('Title', 'Parable of the Sower'),
    label(
      span('Author'),
      () => select(
        { name: 'author_id', required: true, disabled: library.val.authors.length === 0 },
        option({ value: '' }, 'Choose an author'),
        ...library.val.authors.map((author) => option({ value: author.id }, author.name)),
      ),
    ),
    label(
      span('Genre'),
      () => select(
        { name: 'genre_id', required: true, disabled: library.val.genres.length === 0 },
        option({ value: '' }, 'Choose a genre'),
        ...library.val.genres.map((genre) => option({ value: genre.id }, genre.name)),
      ),
    ),
    textField('Publication year', '1993', { type: 'number', min: '1', max: '2100' }),
    button({ type: 'submit', disabled: () => library.val.authors.length === 0 || library.val.genres.length === 0 }, 'Add book'),
  )
}

function RecordsTable(columns, rows, emptyText) {
  if (rows.length === 0) return p({ class: 'empty' }, emptyText)
  return div(
    { class: 'table-wrap' },
    table(
      thead(tr(...columns.map(([heading]) => th(heading)))),
      tbody(...rows.map((row) => tr(...columns.map(([, value]) => td(value(row)))))),
    ),
  )
}

function DataCard(title, count, formNode, renderTable) {
  return section(
    { class: 'data-card' },
    div({ class: 'card-heading' }, h2(title), span({ class: 'count' }, () => String(count()))),
    formNode,
    () => renderTable(),
  )
}

function App() {
  return div(
    header(
      div(
        p({ class: 'eyebrow' }, 'OPENDEPLOY TEST EXAMPLE'),
        h1('Field Notes Library'),
        p({ class: 'intro' }, 'A tiny PostgreSQL catalogue served by one Go binary and a protobuf-powered VanJS interface.'),
      ),
      button(
        { class: 'random', disabled: () => loading.val, onclick: () => run(() => capi.postV1LibraryRandom()) },
        span({ 'aria-hidden': 'true' }, '+'),
        ' Add a random shelf',
      ),
    ),
    main(
      () => error.val ? p({ class: 'error', role: 'alert' }, error.val) : '',
      div(
        { class: 'status-line' },
        span({ class: () => loading.val ? 'status-dot active' : 'status-dot' }),
        () => loading.val ? 'Syncing with PostgreSQL...' : `${library.val.books.length} books catalogued`,
      ),
      div(
        { class: 'grid' },
        DataCard(
          'Authors',
          () => library.val.authors.length,
          AuthorForm(),
          () => RecordsTable([['ID', (row) => row.id], ['Name', (row) => row.name]], library.val.authors, 'No authors yet.'),
        ),
        DataCard(
          'Genres',
          () => library.val.genres.length,
          GenreForm(),
          () => RecordsTable([['ID', (row) => row.id], ['Name', (row) => row.name]], library.val.genres, 'No genres yet.'),
        ),
        section(
          { class: 'data-card books' },
          div({ class: 'card-heading' }, h2('Books'), span({ class: 'count' }, () => String(library.val.books.length))),
          BookForm(),
          () => RecordsTable(
            [
              ['ID', (row) => row.id],
              ['Title', (row) => row.title],
              ['Author', (row) => row.authorName],
              ['Genre', (row) => row.genreName],
              ['Year', (row) => row.publicationYear],
            ],
            library.val.books,
            'No books yet. Add an author and genre first, or generate a random shelf.',
          ),
        ),
      ),
    ),
  )
}

van.add(document.querySelector('#app'), App())
run(() => capi.getV1Library())
