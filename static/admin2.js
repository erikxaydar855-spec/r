/* ITube Admin 2.0 V3: consistent navigation, language/theme controls and simple YouTube intake. */
(function () {
  'use strict';
  const themeKey = 'itube-admin-theme';
  const langKey = 'itube-admin-language';
  const storage = {
    get: (key, fallback) => { try { return localStorage.getItem(key) || fallback; } catch (_) { return fallback; } },
    set: (key, value) => { try { localStorage.setItem(key, value); } catch (_) {} }
  };
  let theme = storage.get(themeKey, 'light') === 'dark' ? 'dark' : 'light';
  let lang = storage.get(langKey, 'uz') === 'ru' ? 'ru' : 'uz';
  document.documentElement.setAttribute('data-admin-theme', theme);
  document.documentElement.lang = lang;

  const navItems = [
    ['/admin/dashboard', 'bi-grid-1x2-fill', 'Dashboard'],
    ['/admin/videos', 'bi-collection-play', 'Videolar'],
    ['/admin/manage', 'bi-building-gear', 'Maktab va sinflar'],
    ['/admin/schools', 'bi-buildings', 'Maktablar ro‘yxati'],
    ['/admin/users', 'bi-people', 'Foydalanuvchilar'],
    ['/', 'bi-globe2', 'Saytga o‘tish'],
    ['/logout', 'bi-box-arrow-right', 'Chiqish']
  ];
  const ru = {
    'Dashboard': 'Панель управления', 'ITube Admin': 'ITube Админ', 'Admin panel': 'Панель администратора',
    'Admin Panel 2.0': 'Панель администратора 2.0', 'Admin kabineti': 'Кабинет администратора',
    'Videolar': 'Видео', 'Videolar boshqaruvi': 'Управление видео', 'Maktab va sinflar': 'Школы и классы',
    'Maktablar ro‘yxati': 'Список школ', "Maktablar ro'yxati": 'Список школ',
    'Foydalanuvchilar': 'Пользователи', 'Saytga o‘tish': 'Открыть сайт', 'Chiqish': 'Выйти',
    'Boshqaruv markazi': 'Центр управления',
    'Ta’lim platformangizdagi videolar, maktablar, sinflar va o‘quvchilar haqida asosiy ko‘rsatkichlar.':
      'Основные показатели видео, школ, классов и учеников вашей образовательной платформы.',
    'Video qo‘shish': 'Добавить видео', 'Maktab qo‘shish': 'Добавить школу',
    'Jami videolar': 'Всего видео', 'Platformadagi barcha videolar': 'Все видео на платформе',
    'Maktablar': 'Школы', 'Ro‘yxatga olingan maktablar': 'Зарегистрированные школы',
    'Sinflar': 'Классы', 'Jami sinf kabinetlari': 'Всего кабинетов классов',
    'O‘quvchilar': 'Ученики', 'Sinflarga biriktirilgan o‘quvchilar': 'Ученики, закреплённые за классами',
    'Ro‘yxatdan o‘tgan hisoblar': 'Зарегистрированные аккаунты',
    'Oxirgi videolar': 'Последние видео',
    'So‘nggi qo‘shilgan videolar, yangilanish sanasi emas': 'По дате добавления, не изменения',
    'Hali video qo‘shilmagan': 'Видео пока не добавлены', 'Barcha videolar': 'Все видео',
    'Kontent holati': 'Состояние контента', 'Video nashr holatining umumiy ulushi': 'Доля опубликованных видео',
    'Nashr qilingan': 'Опубликовано', 'Nashr': 'Опубликовано', 'Qoralama': 'Черновик',
    'Kategoriyalar': 'Категории', 'Foydalanuvchi hisoblari': 'Аккаунты пользователей',
    'Maktablar va sinflar boshqaruvi': 'Управление школами и классами',
    'Barcha maktablar': 'Все школы', 'Maktab nomi': 'Название школы',
    'Maktab': 'Школа', 'Viloyat': 'Регион', 'Email': 'Электронная почта',
    'Qo‘shish': 'Добавить', 'Yangi maktab qo‘shish': 'Добавить новую школу',
    '➕ Yangi maktab qo‘shish': '➕ Добавить новую школу',
    'Maktab uchun yangi parol': 'Новый пароль школы', 'Sinf uchun yangi parol': 'Новый пароль класса',
    'Maktablar ro‘yxati': 'Список школ', '🏫 Maktablar ro‘yxati': '🏫 Список школ',
    'Sinf raqami': 'Номер класса', 'Harf': 'Буква', 'Sinf sardori': 'Староста класса',
    'Sinf rahbari': 'Классный руководитель', 'Sinf': 'Класс',
    'Rahbar': 'Руководитель', 'Sardor': 'Староста', 'Harakatlar': 'Действия',
    'Harakat': 'Действие', 'Tahrirlash': 'Изменить', 'O‘chirish': 'Удалить',
    'O‘quvchi qo‘shish': 'Добавить ученика', 'F.I.SH': 'Ф. И. О.',
    'Tug‘ilgan yil': 'Год рождения', 'Jinsi': 'Пол', 'O‘g‘il': 'Мальчик', 'Qiz': 'Девочка',
    'Manzil': 'Адрес', 'Manzil (yashash joyi)': 'Адрес проживания',
    'Asl qayerlik': 'Откуда родом', 'Dars jadvali': 'Расписание уроков',
    '📅 Dars jadvali': '📅 Расписание уроков', '📅 Jadval': '📅 Расписание',
    'Hali maktablar yo‘q': 'Школ пока нет', 'Hali sinflar yo‘q': 'Классов пока нет',
    'Hali o‘quvchilar yo‘q': 'Учеников пока нет',
    'Hali sinflar mavjud emas.': 'Классов пока нет.',
    'Hali video yo‘q': 'Видео пока нет', 'Kategoriya': 'Категория', 'Jami': 'Всего',
    'Holat': 'Статус', 'Holati': 'Статус', 'Vaqt': 'Время', 'ID': 'ID',
    'Yangi video qo‘shish': 'Добавить новое видео', 'Sarlavha': 'Название',
    'Tavsif': 'Описание', 'Video URL': 'Ссылка на видео',
    'Nashr etish': 'Опубликовать', 'Nashr qilish': 'Опубликовать',
    'Saqlash': 'Сохранить', 'Bekor qilish': 'Отмена', 'Orqaga': 'Назад',
    'Ro‘yxatdan o‘tgan foydalanuvchilar': 'Зарегистрированные пользователи',
    'Username': 'Имя пользователя', 'Ko‘rgan': 'Просмотрено', 'O‘rtacha %': 'Средний %',
    'Foydalanuvchilar yo‘q': 'Пользователей пока нет',
    'Tezkor boshqaruv': 'Быстрое управление',
    'Maktab va sinflarni boshqarish': 'Управление школами и классами',
    'Videolarni boshqarish': 'Управление видео',
    'Bosh sahifaga qaytish': 'Вернуться на главную',
    'Boshqaruv markaziga qaytish': 'Вернуться в центр управления',
    '← Boshqaruv markaziga qaytish': '← Вернуться в центр управления',
    'Video saqlandi.': 'Видео сохранено.',
    'Holatini quyida tekshiring.': 'Проверьте статус ниже.',
    'Qoralama admin ko‘rib chiqishi uchun yuborildi.': 'Черновик отправлен на проверку администратору.',
    'Xodim hisobi yaratildi. Xodimga login va parolni xavfsiz tarzda bering.':
      'Учётная запись сотрудника создана. Безопасно передайте ему логин и пароль.',
    'YouTube orqali video qo‘shish': 'Добавление видео с YouTube',
    'Havola → ma’lumotlarni olish → fan → saqlash. Video fayli serverga yuklanmaydi.':
      'Ссылка → данные → предмет → сохранить. Видеофайл не загружается на сервер.',
    '1. YouTube havolasi': '1. Ссылка YouTube',
    'Ma’lumotlarni olish': 'Получить данные',
    'Havolani qo‘ying. Sarlavha va muqova imkon bo‘lsa avtomatik to‘ldiriladi.':
      'Вставьте ссылку. Название и обложка заполнятся автоматически, если доступны.',
    '2. Sarlavha': '2. Название', '3. Fan': '3. Предмет',
    '— Fanni tanlang —': '— Выберите предмет —',
    'Tavsif (ixtiyoriy)': 'Описание (необязательно)',
    'Nashr holati': 'Статус публикации',
    'Qoralama — tekshirish uchun': 'Черновик — на проверку',
    'Videoni saqlash': 'Сохранить видео',
    'Xodim uchun video kiritish hisobi yaratish': 'Создать аккаунт сотрудника для добавления видео',
    '(faqat qoralama qo‘sha oladi)': '(может добавлять только черновики)',
    'Bu hisob faqat videolar ro‘yxatini ko‘radi va yangi videoni qoralama sifatida jo‘natadi. Maktablar, foydalanuvchilar, tahrirlash va o‘chirish ruxsatlari yo‘q.':
      'Аккаунт может просматривать список видео и отправлять черновики. Нет доступа к школам, пользователям, редактированию и удалению.',
    'Xodim login nomi': 'Логин сотрудника',
    'Xodim paroli (kamida 14 belgi)': 'Пароль сотрудника (не менее 14 символов)',
    'Yaratish': 'Создать', 'Faqat ko‘rish': 'Только просмотр',
    'Videongiz qoralama sifatida yuboriladi. Admin tekshirganidan keyin nashr qiladi.':
      'Ваше видео будет отправлено как черновик. Администратор опубликует его после проверки.',
    'Qidirish...': 'Поиск...', 'Videolarni qidirish...': 'Поиск видео...',
    'Tahrirlash:': 'Редактирование:', 'Batafsil': 'Подробнее',
    'Kunlik': 'Ежедневно', 'Oylik': 'Ежемесячно', 'Yillik': 'Ежегодно',
    'Faol': 'Активных', 'Faollik darajasi': 'Уровень активности',
    'Fan:': 'Предмет:', 'Bu sinf hali hech qanday video ko‘rmagan.': 'Этот класс ещё не смотрел видео.',
    'Har bir kun uchun darslar soni kiriting. Bu kunlik video ko‘rish foizini hisoblashda ishlatiladi.':
      'Укажите число уроков для каждого дня. Это используется для расчёта процента просмотра видео за день.',
    'Muhim:': 'Важно:', 'dars': 'урок',
    'Video muqovasi': 'Обложка видео',
    'Avtomatik to‘ldiriladi yoki qo‘lda yozing': 'Заполнится автоматически или вручную',
    'Bu darsda nimalar o‘rganiladi?': 'Что ученики узнают на этом уроке?',
    'Qoralama admin uchun': 'Черновик для администратора',
    'Profil': 'Профиль', 'Bosh sahifa': 'Главная', 'Mening sinflarim': 'Мои классы'
  };
  const originals = new WeakMap();
  const originalAttrs = new WeakMap();
  function translate(root) {
    const walker = document.createTreeWalker(root || document.body, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) {
      if (!node.parentElement || /^(SCRIPT|STYLE|TEXTAREA|CODE)$/i.test(node.parentElement.tagName) || node.parentElement.closest('.admin2-prefs')) continue;
      if (!originals.has(node)) originals.set(node, node.nodeValue);
      const original = originals.get(node);
      const trimmed = original.trim().replace(/\s+/g, ' ');
      const translated = lang === 'ru' ? ru[trimmed] : null;
      if (translated) {
        const prefix = (original.match(/^\s*/) || [''])[0];
        const suffix = (original.match(/\s*$/) || [''])[0];
        node.nodeValue = prefix + translated + suffix;
      } else {
        node.nodeValue = original;
      }
    }
    (root || document).querySelectorAll('[placeholder],[aria-label],[title]').forEach(el => {
      if (!originalAttrs.has(el)) originalAttrs.set(el, {
        placeholder: el.getAttribute('placeholder'),
        aria: el.getAttribute('aria-label'),
        title: el.getAttribute('title')
      });
      const orig = originalAttrs.get(el);
      for (const [attr, value] of [['placeholder', orig.placeholder], ['aria-label', orig.aria], ['title', orig.title]]) {
        if (value != null) el.setAttribute(attr, lang === 'ru' ? (ru[value] || value) : value);
      }
    });
    document.documentElement.lang = lang;
  }

  function makeIcon(name) {
    const el = document.createElement('i');
    el.className = 'bi ' + name;
    el.setAttribute('aria-hidden', 'true');
    return el;
  }
  function buildNav(nav, uploader) {
    const path = location.pathname.replace(/\/$/, '') || '/';
    nav.replaceChildren();
    const items = uploader ? navItems.filter(it => ['/admin/videos', '/', '/logout'].includes(it[0])) : navItems;
    items.forEach(([href, icon, label]) => {
      const a = document.createElement('a');
      a.href = href; a.className = 'nav-link';
      const active = href === '/admin/videos' ? path === href || path.startsWith('/admin/videos/') :
        path === href || (href === '/admin/schools' && path.startsWith('/admin/schools/'));
      if (active) { a.classList.add('active'); a.setAttribute('aria-current', 'page'); }
      a.append(makeIcon(icon), document.createTextNode(label));
      nav.append(a);
    });
  }
  function makePreferenceControls() {
    const prefs = document.createElement('div'); prefs.className = 'admin2-prefs';
    const themeBtn = document.createElement('button'); themeBtn.type = 'button'; themeBtn.className = 'admin2-pref';
    const langBtn = document.createElement('button'); langBtn.type = 'button'; langBtn.className = 'admin2-pref';
    function draw() {
      document.documentElement.setAttribute('data-admin-theme', theme);
      themeBtn.replaceChildren(makeIcon(theme === 'dark' ? 'bi-sun' : 'bi-moon-stars'), document.createTextNode(theme === 'dark' ? (lang === 'ru' ? 'День' : 'Kunduzgi') : (lang === 'ru' ? 'Ночь' : 'Tungi')));
      themeBtn.setAttribute('aria-label', theme === 'dark' ? 'Kunduzgi rejim' : 'Tungi rejim');
      langBtn.replaceChildren(makeIcon('bi-translate'), document.createTextNode(lang === 'ru' ? '🇷🇺 Русский' : '🇺🇿 O‘zbekcha'));
      langBtn.title = lang === 'ru' ? 'O‘zbekchaga o‘tish' : 'Переключить на русский';
      document.documentElement.lang = lang;
    }
    themeBtn.addEventListener('click', () => { theme = theme === 'light' ? 'dark' : 'light'; storage.set(themeKey, theme); draw(); });
    langBtn.addEventListener('click', () => { lang = lang === 'uz' ? 'ru' : 'uz'; storage.set(langKey, lang); draw(); translate(document.body); });
    prefs.append(langBtn, themeBtn); draw(); return prefs;
  }

  document.addEventListener('DOMContentLoaded', function () {
    if (!location.pathname.startsWith('/admin/')) return;
    const uploader = document.body.dataset.adminRole === 'uploader';
    const shell = document.querySelector('body > .container-fluid.p-0 > .row.g-0');
    const main = shell && shell.querySelector(':scope > .main-content');
    const sidebar = shell && shell.querySelector(':scope > .sidebar');
    let nav = sidebar && sidebar.querySelector('nav.nav');
    const prefs = makePreferenceControls();

    if (main && sidebar) {
      if (!nav) { nav = document.createElement('nav'); nav.className = 'nav flex-column'; sidebar.append(nav); }
      buildNav(nav, uploader);
      const toolbar = document.createElement('div'); toolbar.className = 'admin2-toolbar';
      const drawer = document.createElement('button'); drawer.type = 'button'; drawer.className = 'admin2-drawer-toggle';
      drawer.setAttribute('aria-expanded', 'false'); drawer.append(makeIcon('bi-list'), document.createTextNode('Menyu'));
      const crumb = document.createElement('div'); crumb.className = 'admin2-crumbs';
      const home = document.createElement('a'); home.href = uploader ? '/admin/videos' : '/admin/dashboard'; home.textContent = 'ITube Admin';
      const separator = document.createElement('span'); separator.textContent = '/';
      const current = document.createElement('b');
      const path = location.pathname;
      current.textContent = path.startsWith('/admin/videos') ? 'Videolar' : path.startsWith('/admin/manage') ? 'Maktab va sinflar' : path.startsWith('/admin/schools') ? 'Maktablar' : path.startsWith('/admin/users') ? 'Foydalanuvchilar' : 'Boshqaruv markazi';
      crumb.append(home, separator, current);
      const actions = document.createElement('div'); actions.className = 'admin2-toolbar-actions'; actions.append(prefs);
      if (main.querySelector('table')) {
        const searchWrap = document.createElement('label'); searchWrap.className = 'admin2-search-wrap';
        searchWrap.append(makeIcon('bi-search'));
        const input = document.createElement('input'); input.type = 'search'; input.placeholder = 'Qidirish...'; input.setAttribute('aria-label', 'Qidirish...');
        searchWrap.append(input); actions.prepend(searchWrap);
        input.addEventListener('input', () => {
          const text = input.value.trim().toLocaleLowerCase();
          main.querySelectorAll('table tbody tr').forEach(row => {
            row.hidden = !!text && !row.textContent.toLocaleLowerCase().includes(text);
          });
        });
        main.querySelectorAll('table').forEach(table => {
          if (!table.closest('.table-responsive')) {
            const wrapper = document.createElement('div'); wrapper.className = 'table-responsive';
            table.parentNode.insertBefore(wrapper, table); wrapper.append(table);
          }
        });
      }
      toolbar.append(drawer, crumb, actions); main.prepend(toolbar);
      const backdrop = document.createElement('div'); backdrop.className = 'admin2-drawer-backdrop'; document.body.append(backdrop);
      function closeMenu() { document.body.classList.remove('admin2-menu-open'); drawer.setAttribute('aria-expanded', 'false'); }
      drawer.addEventListener('click', () => { const opened = document.body.classList.toggle('admin2-menu-open'); drawer.setAttribute('aria-expanded', String(opened)); });
      backdrop.addEventListener('click', closeMenu);
      document.addEventListener('keydown', e => { if (e.key === 'Escape') closeMenu(); });
      nav.querySelectorAll('a').forEach(a => a.addEventListener('click', closeMenu));
    } else {
      // Smaller edit/list pages still receive navigation and persistent preferences.
      const mini = document.createElement('div'); mini.className = 'admin2-compact-bar';
      const links = document.createElement('nav'); links.className = 'admin2-compact-links';
      const items = uploader ? navItems.filter(it => ['/admin/videos', '/', '/logout'].includes(it[0])) : navItems;
      items.forEach(([href, icon, label]) => {
        const a = document.createElement('a'); a.href = href; a.append(makeIcon(icon), document.createTextNode(label)); links.append(a);
      });
      mini.append(links, prefs); document.body.prepend(mini);
    }
    translate(document.body);
    initVideoWizard();
  });

  function initVideoWizard() {
    const form = document.getElementById('admin2-video-form'); if (!form) return;
    const urlField = form.querySelector('#video-url');
    const title = form.querySelector('#video-title');
    const body = form.querySelector('#video-body');
    const button = form.querySelector('#admin2-fetch-meta');
    const msg = form.querySelector('#admin2-meta-message');
    const preview = form.querySelector('#admin2-video-preview');
    const thumb = form.querySelector('#admin2-video-thumb');
    const previewTitle = form.querySelector('#admin2-video-preview-title');
    const previewAuthor = form.querySelector('#admin2-video-preview-author');
    let aborter, debounce, lastURL = '';
    function message(uz, ruText) { msg.textContent = lang === 'ru' ? ruText : uz; }
    async function fillMetadata() {
      const value = urlField.value.trim(); if (!value || value === lastURL) return;
      lastURL = value;
      if (aborter) aborter.abort(); aborter = new AbortController();
      message('YouTube ma’lumoti olinmoqda...', 'Загружаются данные YouTube...');
      button.disabled = true;
      preview.hidden = true;
      try {
        const res = await fetch('/admin/videos/metadata?url=' + encodeURIComponent(value), {
          credentials: 'same-origin', signal: aborter.signal, headers: {'Accept':'application/json'}
        });
        if (!res.ok) throw new Error('metadata');
        const data = await res.json();
        if (value !== urlField.value.trim()) return;
        if (!title.value.trim()) title.value = data.title || '';
        if (!body.value.trim() && data.author) body.value = (lang === 'ru' ? 'Автор: ' : 'Muallif: ') + data.author;
        if (data.thumbnail && data.thumbnail.startsWith('https://img.youtube.com/')) {
          thumb.src = data.thumbnail; previewTitle.textContent = data.title || '';
          previewAuthor.textContent = data.author || ''; preview.hidden = false;
        }
        message('Tayyor! Sarlavha, fan va tavsifni tekshirib saqlang.', 'Готово! Проверьте название, предмет и описание.');
      } catch (e) {
        if (e.name !== 'AbortError') message('Avtomatik olish imkonsiz. Sarlavhani qo‘lda kiriting.', 'Не удалось получить данные. Введите название вручную.');
      } finally { button.disabled = false; }
    }
    urlField.addEventListener('input', () => { clearTimeout(debounce); lastURL = ''; preview.hidden = true; debounce = setTimeout(fillMetadata, 700); });
    urlField.addEventListener('paste', () => { clearTimeout(debounce); debounce = setTimeout(fillMetadata, 300); });
    button.addEventListener('click', () => { lastURL = ''; fillMetadata(); });
    form.addEventListener('submit', () => {
      const save = document.getElementById('admin2-save-video');
      if (save) { save.disabled = true; save.setAttribute('aria-busy', 'true'); }
    });
  }
})();
