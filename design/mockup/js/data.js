/* Sample data. Everything here is invented. Shapes mirror docs/design.md section 7. */
const KINDS={
  article:{n:'Article',i:'newspaper',lib:412},product:{n:'Product',i:'shopping-bag',lib:96},book:{n:'Book',i:'book-open',lib:58},
  recipe:{n:'Recipe',i:'utensils',lib:121},video:{n:'Video',i:'play',lib:143},tweet:{n:'Tweet',i:'message-circle',lib:77},
  note:{n:'Note',i:'sticky-note',lib:164},image:{n:'Image',i:'image',lib:89},paper:{n:'Paper',i:'file-text',lib:34},
  podcast:{n:'Podcast',i:'mic',lib:27},place:{n:'Place',i:'map-pin',lib:41},movie:{n:'Movie',i:'film',lib:22}
};
const SOURCES={app:{n:'In-app',i:'monitor'},telegram:{n:'Telegram',i:'send'},shortcut:{n:'iOS Shortcut',i:'smartphone'},quick:{n:'Quick-save page',i:'zap'},import:{n:'Import',i:'upload'}};
const HL={amber:'#f2b866',magenta:'#ff5f93',teal:'#58d4a8',blue:'#6ea8ff'};
const HL_NAMES=Object.keys(HL);

const WAL_BODY=[
 ['p','Most databases make you choose between readers and writers. Write-ahead logging is the quiet trick that lets a small embedded database avoid that choice for most workloads.'],
 ['h','How the log works'],
 ['p','Instead of changing pages in the main database file, a writer appends the changed pages to a separate log. Readers keep reading the old pages until the log is folded back in, so a long query never blocks the next write.'],
 ['p','Each reader notes the last commit in the log when it starts and ignores anything newer. That small rule gives every reader a consistent snapshot without taking a lock on the writer.'],
 ['h','Checkpoints are where it gets interesting'],
 ['p','Eventually the log has to be copied back into the database. A checkpoint does that work, and it can only reclaim log space that no reader still needs. One long-running read transaction can keep the log growing for hours.'],
 ['p','The practical advice is dull and effective: keep read transactions short, let the automatic checkpoint run, and watch the size of the log file the way you would watch disk space.'],
 ['h','What it costs you'],
 ['p','The trade is a second file next to the database and a requirement that every process shares memory on the same machine. For a single-user library that lives on one box, neither of those matters.']
];
const GENERIC_BODY=[['p','This is the extracted reader text for the article. In the real app it comes from go-readability, stored in content_text and versioned so highlights can re-anchor after a re-extraction.'],['p','Highlights are stored as a text quote with a prefix and suffix, never as character offsets, so a small change to the page does not silently move them.'],['p','If a quote cannot be found again, the highlight is listed as detached rather than guessed at.']];

/* tags: a = AI assigned, m = manual. ago = days since saved. */
const ITEMS=[
 {id:'a1',kind:'article',title:'Write-Ahead Logging',url:'sqlite.org/wal',site:'sqlite.org',ago:2,saved:'Nov 1',src:'telegram',a:['sqlite','databases','storage'],m:['homelab'],rl:1,
  sum:'How WAL mode lets readers and one writer work at the same time, and what makes a checkpoint stall. The advice is short transactions and watching the log file size.',
  note:'Relevant to **Harbor**: the potato runs one writer plus the UI. Check `PRAGMA wal_autocheckpoint` before the import.',
  cv:{t:'grad',ar:1.6,c:['#1d4e63','#0b2433','#eaa84a']},d:{read:'9 min',words:'2,140',author:'Sqlite docs team',body:WAL_BODY},
  hl:[{id:'h1',color:'amber',quote:'a long query never blocks the next write',note:'This is the whole pitch.'},{id:'h2',color:'magenta',quote:'One long-running read transaction can keep the log growing for hours.',note:'Add to Status page: log size.'},{id:'h3',color:'teal',quote:'the original paragraph about shared memory locks',note:'',detached:1}]},
 {id:'r1',kind:'recipe',title:'Burnt-butter miso cookies',url:'saltandslate.example/miso-cookies',site:'saltandslate.example',ago:3,saved:'Oct 31',src:'shortcut',a:['baking','miso','cookies'],m:[],
  sum:'Brown the butter until it smells like toast, then fold in white miso for a salty edge. Chill the dough overnight for the best spread.',note:'',cv:{t:'grad',ar:.82,c:['#8a4b27','#3a1d12','#f0c27a']},
  d:{time:'35 min',yield:'18 cookies',ing:[['225 g','butter'],['2 tbsp','white miso'],['200 g','brown sugar'],['1','egg + 1 yolk'],['280 g','flour'],['150 g','dark chocolate']],steps:['Brown the butter, then cool it for 15 minutes.','Whisk in the miso, sugar, egg and yolk.','Fold in the flour and chocolate, then chill overnight.','Bake at 180 °C for 11 minutes and rest on the tray.']}},
 {id:'r2',kind:'recipe',title:'Chili crisp noodles',url:'saltandslate.example/chili-noodles',site:'saltandslate.example',ago:9,saved:'Oct 25',src:'app',a:['noodles','chili'],m:['weeknight'],sum:'Fifteen-minute noodles with a spoonful of chili crisp, black vinegar and scallions.',note:'',cv:{t:'grad',ar:1.25,c:['#7a2a1a','#2a0f0a','#ff9a5c']},d:{time:'15 min',yield:'2 bowls',ing:[['200 g','wheat noodles'],['2 tbsp','chili crisp'],['1 tbsp','black vinegar']],steps:['Boil the noodles.','Toss with chili crisp and vinegar.']}},
 {id:'v1',kind:'video',title:'Building a keyboard from scratch',url:'youtube.com/watch?v=k9xQ',site:'youtube.com',ago:4,saved:'Oct 30',src:'telegram',a:['hardware','making','keyboards'],m:[],
  sum:'A maker walks through designing a split keyboard: switch choice, a hand-routed PCB and the firmware layer map.',note:'',cv:{t:'grad',ar:1.78,c:['#35355f','#16162c','#ff7a59']},
  d:{dur:'24:18',channel:'Switchback Labs',ts:'ok',chap:[['0:00','Why build one'],['3:42','Switches and spacing'],['9:15','PCB routing'],['17:30','Firmware layers']],lines:[['0:00','So the goal today is a keyboard that fits my hands, not the other way around.'],['3:42','Pick the switch first. Everything else, the plate, the spacing, the case, follows from it.'],['9:15','Keep the matrix traces short and route the columns on the back layer.'],['17:30','Layer one is letters, layer two is symbols, and the thumb keys do the switching.']]}},
 {id:'v2',kind:'video',title:'Soldering for people who hate soldering',url:'youtube.com/watch?v=s0ld',site:'youtube.com',ago:12,saved:'Oct 22',src:'quick',a:['hardware','making','rpi'],m:[],sum:'Short, practical soldering tips: temperature, flux and when to give up and reflow.',note:'',cv:{t:'grad',ar:1.78,c:['#3a2a1a','#12100c','#ffc36b']},d:{dur:'11:02',channel:'Bench Notes',ts:'unavailable',chap:[],lines:[]}},
 {id:'n1',kind:'note',title:'SD card wear',url:'',site:'Quick-save page',ago:4,saved:'Oct 30',src:'quick',a:[],m:['homelab','todo'],sum:'',note:'Move media off the SD card before the next import.\n\nCheck wear on the potato first. `smartctl` will not see it, so compare **write totals** month over month.\n\n- [ ] ask about wear levelling\n- [ ] decide where media lives',cv:null,d:{}},
 {id:'n2',kind:'note',title:'Spaces idea',url:'',site:'In-app',ago:6,saved:'Oct 28',src:'app',a:[],m:['harbor'],sum:'',note:'A Space for **things I want to try this month**. Auto-expires? Probably not, expiry feels like a queue.',cv:null,d:{}},
 {id:'p1',kind:'product',title:'Orbit trackball, slate',url:'tinkerlab.example/orbit',site:'tinkerlab.example',ago:5,saved:'Oct 29',src:'shortcut',a:['desk','hardware'],m:[],sum:'A 34 mm ball trackball with a matte slate shell and four programmable buttons. Wired or Bluetooth.',note:'',cv:{t:'prod',ar:1,c:['#2b2f38','#1a1c22','#7e8ea8']},
  d:{price:'$89',avail:'In stock',brand:'Tinker Lab',hist:[99,99,94,94,89,89,92,89],specs:[['Ball','34 mm'],['Buttons','4'],['Connection','USB-C / Bluetooth'],['Weight','212 g']]}},
 {id:'p2',kind:'product',title:'NVMe hat for Raspberry Pi 5',url:'tinkerlab.example/nvme-hat',site:'tinkerlab.example',ago:15,saved:'Oct 19',src:'app',a:['hardware','homelab','storage','raspberry-pi'],m:[],sum:'A PCIe adapter that mounts a 2230 or 2242 NVMe drive under a Raspberry Pi 5.',note:'',cv:{t:'prod',ar:1.1,c:['#2a3530','#161d1a','#58d4a8']},d:{price:'$24',avail:'Backorder',brand:'Tinker Lab',hist:[24,24,24,26,26,24],specs:[['Interface','PCIe 2.0 x1'],['Drive','2230 / 2242']]}},
 {id:'t1',kind:'tweet',title:'@fieldwork',url:'x.com/fieldwork/status/1',site:'x.com',ago:6,saved:'Oct 28',src:'telegram',a:['software','design'],m:[],sum:'',note:'',cv:null,d:{text:'The best personal software has exactly one button, and it says Save.',handle:'@fieldwork',name:'Field Work'}},
 {id:'pp1',kind:'paper',title:'Attention Residuals at Small Scale',url:'arxiv.example/abs/2610.04412',site:'arxiv.example',ago:8,saved:'Oct 26',src:'app',a:['ml','papers'],m:[],sum:'Shows that adding residual attention paths helps models below 100M parameters more than larger ones.',note:'',cv:{t:'page',ar:.78,c:['#e8e4d8','#cfc9b8','#4a6fa5']},
  d:{authors:'Lena Brandt, Omar Aziz',venue:'arXiv 2610.04412',pages:14,text:'ok',abstract:'We study residual attention paths in transformers under 100M parameters and find consistent gains on small-scale benchmarks that shrink as models grow.'}},
 {id:'b1',kind:'book',title:'The Timeless Way of Building',url:'',site:'Christopher Alexander',ago:9,saved:'Oct 25',src:'app',a:['architecture','patterns'],m:[],rl:1,sum:'A theory of how buildings and towns get their quality, built on patterns people recognise without a name for them.',note:'',cv:{t:'book',ar:.68,c:['#3b2f2a','#1b1512','#d9b36c'],tt:['THE TIMELESS','WAY OF','BUILDING'],au:'CHRISTOPHER ALEXANDER'},d:{author:'Christopher Alexander',year:'1979',pages:'552',isbn:'978-0195024029',publisher:'Oxford University Press'}},
 {id:'b2',kind:'book',title:'A Pattern Language',url:'',site:'Christopher Alexander',ago:30,saved:'Oct 4',src:'import',a:['architecture','patterns'],m:[],sum:'253 patterns for towns, buildings and construction, each linking to the patterns around it.',note:'',cv:{t:'book',ar:.68,c:['#243a36','#101c1a','#9fd9c6'],tt:['A PATTERN','LANGUAGE'],au:'CHRISTOPHER ALEXANDER'},d:{author:'Christopher Alexander',year:'1977',pages:'1171',isbn:'978-0195019193',publisher:'Oxford University Press'}},
 {id:'po1',kind:'podcast',title:'Compiler Tales 112: Incremental parsing',url:'overcast.example/compiler-tales/112',site:'overcast.example',ago:9,saved:'Oct 25',src:'shortcut',a:['programming'],m:[],sum:'Two language-tool authors discuss reparsing only what changed, and why editors care.',note:'',cv:{t:'wave',ar:1,c:['#2a4a3f','#10241d','#8fe0b9']},d:{show:'Compiler Tales',ep:112,dur:'52 min',ts:'published',notes:'Guests: Priya Nair and Tom Eklund. Topics: tree-sitter, error recovery, incremental lexing.'}},
 {id:'pl1',kind:'place',title:'Café Orrery',url:'',site:'Lisbon',ago:14,saved:'Oct 20',src:'telegram',a:['travel','coffee'],m:[],sum:'A narrow Lisbon café with a good flat white and a small shelf of astronomy books.',note:'',cv:{t:'map',ar:1.33,c:['#13262b','#2d4a52','#eaa84a']},d:{addr:'Rua do Norte 41, Lisbon',hours:'Open until 18:00',rating:'4.7',phone:'+351 21 000 0000'}},
 {id:'m1',kind:'movie',title:'Perfect Days',url:'',site:'Wim Wenders',ago:14,saved:'Oct 20',src:'app',a:['film'],m:[],sum:'A Tokyo toilet cleaner finds small joys in a repeated daily routine.',note:'',cv:{t:'poster',ar:.67,c:['#e48a4f','#3d2352','#ffd9a0'],tt:['PERFECT','DAYS']},d:{year:'2023',dir:'Wim Wenders',runtime:'2h 03m',cast:'Kōji Yakusho, Tokio Emoto',where:'Streaming in some regions'}},
 {id:'i1',kind:'image',title:'Moss on a slate wall, overcast light',url:'',site:'Telegram',ago:14,saved:'Oct 20',src:'telegram',a:['photo','texture'],m:[],sum:'',note:'',cv:{t:'tex',ar:.75,c:['#2c3a2f','#7fa070','#1a2220']},d:{desc:'Moss growing across a dark slate wall in flat, overcast light.',ocr:'',colors:['#2c3a2f','#7fa070','#1a2220','#a9b99a'],dims:'3024 × 4032',bytes:'2.8 MB',sha:'9f2c71…b0e4',mirrored:true}},
 {id:'i2',kind:'image',title:'Screenshot: Meridian tile for Harbor',url:'',site:'Pasted in app',ago:1,saved:'Nov 2',src:'app',a:['homelab','screenshot'],m:['harbor'],sum:'',note:'',cv:{t:'grad',ar:1.9,c:['#102a2e','#091214','#58d4a8']},d:{desc:'A dashboard tile reading Harbor with a green status dot and the number 12 labelled saved this week.',ocr:'Harbor · healthy · saved this week 12 · restart · update',colors:['#102a2e','#58d4a8','#091214','#e8ece6'],dims:'1280 × 674',bytes:'184 KB',sha:'41ab09…77d1',mirrored:false}},
 {id:'a2',kind:'article',title:'Notes on running a home server for one person',url:'oldforum.example/thread/221',site:'oldforum.example',ago:21,saved:'Oct 13',src:'import',a:['homelab'],m:[],dead:1,sum:'A long thread on fan curves, SD cards and why uptime stopped mattering.',note:'',cv:{t:'grad',ar:1.2,c:['#4a3a2a','#1d1610','#c9915a']},d:{read:'14 min',words:'3,480',author:'various',body:GENERIC_BODY},hl:[]},
 {id:'a3',kind:'article',title:'Tailscale serve, explained',url:'netnotes.example/tailscale-serve',site:'netnotes.example',ago:7,saved:'Oct 27',src:'app',a:['homelab','tailscale','networking'],m:[],sum:'What tailscale serve does, how it differs from funnel, and why loopback-only ports are the safe default.',note:'',cv:{t:'grad',ar:1.5,c:['#2a2f4f','#10121f','#8aa0ff']},d:{read:'6 min',words:'1,420',author:'Net Notes',body:GENERIC_BODY},hl:[]},
 {id:'a4',kind:'article',title:'Why SQLite fits one-person software',url:'localfirst.example/sqlite-one-person',site:'localfirst.example',ago:18,saved:'Oct 16',src:'import',a:['sqlite','local-first','databases'],m:[],sum:'The case for a single-file database when the only user is you.',note:'',cv:{t:'grad',ar:1.3,c:['#31423a','#101a16','#b7e08a']},d:{read:'7 min',words:'1,780',author:'Local First',body:GENERIC_BODY},hl:[]}
];
const PENDING=[{id:'q1',kind:'article',title:'arm64 boards worth buying in 2026',site:'github.com',src:'telegram',age:'12s',lbl:'Saving from Telegram'}];
const FAILED=[{id:'f1',url:'oldforum.example/thread/221',err:'Timed out after 15 s (page larger than the 5 MB cap)',tries:3,at:'Nov 3, 09:41',src:'import'}];

/* Spaces: type auto|saved|manual. rule fields are ANDed; tags_any / kinds / sources are ORed inside. */
const SPACES=[
 {id:'later',n:'Read later',t:'manual',lib:14,ids:null,desc:'Hand-picked. Not a queue.'},
 {id:'hw',n:'Desk & hardware',t:'auto',lib:63,rule:{tags_any:['hardware','desk']},desc:'Grows by itself.'},
 {id:'lab',n:'Homelab',t:'saved',lib:88,rule:{tags_any:['homelab','tailscale','sqlite']},desc:'A saved search.'},
 {id:'kitchen',n:'Kitchen',t:'auto',lib:129,rule:{tags_any:['baking','noodles','coffee','miso']},desc:'Grows by itself.'},
 {id:'recent',n:'Recent reading',t:'auto',lib:31,rule:{kinds:['article','paper'],within:14},desc:'Articles and papers from the last two weeks.'},
 {id:'refs',n:'Design refs',t:'manual',lib:210,ids:['t1','b1','b2','m1','pl1'],desc:'Imported from a Raindrop collection.',imported:true}
];
const TAG_ALIASES={'sqlite3':'sqlite','SQLite':'sqlite','home-lab':'homelab','local first':'local-first'};
const SUGGESTION={name:'Raspberry Pi & SBCs',why:'You saved 6 items about single-board computers this month.',rule:{tags_any:['hardware','storage'],within:30},count:6};
const DELETED0=[{id:'d1',kind:'article',title:'Ten tips for faster Docker builds',site:'devblog.example',left:23,cv:{t:'grad',ar:1.5,c:['#233a5a','#0b1626','#6ea8ff']}},{id:'d2',kind:'recipe',title:'Overnight focaccia',site:'saltandslate.example',left:11,cv:{t:'grad',ar:1.2,c:['#6b5a2a','#221c0c','#ffd77a']}},{id:'d3',kind:'note',title:'Untitled note',site:'In-app',left:2,cv:null}];
const DOORS=[
 {id:'telegram',n:'Telegram bot',i:'send',desc:'Links, text, photos, documents and albums.',last:'12 s ago',made:'Oct 2',tok:'tg_••••9f3a'},
 {id:'shortcut',n:'iOS Shortcut and share sheet',i:'smartphone',desc:'The share sheet in every app. Also the PWA share target on Android.',last:'3 days ago',made:'Oct 2',tok:'sh_••••c81e'},
 {id:'quick',n:'Quick-save page',i:'zap',desc:'One input, pinned to the home screen. Tailnet only.',last:'4 days ago',made:'Oct 2',tok:'qk_••••027b'}
];
const BACKUPS=[['Nov 3, 03:00','48.2 MB','R2 + MacBook Air'],['Nov 2, 03:00','48.0 MB','R2 + MacBook Air'],['Nov 1, 03:00','47.7 MB','R2 only (Air offline)'],['Oct 31, 03:00','47.5 MB','R2 + MacBook Air']];
const STATUS={poll:'8 s ago',queue:1,mirror:0,mirrorBytes:'0 B',backup:'Nov 3, 03:00',disk:41,links:'Nov 3, 04:00',linkSlice:'50 checked, 1 dead',version:'r482.9f3c1a2',saves:[3,5,2,6,4,7,2,9,5,12],worker:'ok',items:1284,week:12};

/* mutable app state */
const S={
  items:ITEMS.map(x=>({...x,a:[...x.a],m:[...x.m],hl:(x.hl||[]).map(h=>({...h}))})),
  pending:PENDING.map(x=>({...x})),failed:FAILED.map(x=>({...x})),
  spaces:SPACES.map(x=>({...x,rule:x.rule&&{...x.rule}})),
  deleted:DELETED0.map(x=>({...x})),
  tagAlias:{...TAG_ALIASES},
  suggestion:{...SUGGESTION},
  ui:{q:'',kind:'all',space:null,tag:null,source:null,date:null,sel:new Set(),selmode:false,phone:false},
  update:{state:'idle'},       // idle | checking | available | updating | done
  imported:false,firstRun:false,dismissAttn:false
};
