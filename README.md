# Unicorn Overlord UNBURGER

An English re-translation and restoration patch for the Western Nintendo Switch release of **Unicorn Overlord**.

## Why we made this patch

The English release of Unicorn Overlord changes far more than names, honorifics and a few isolated lines.

Once we started comparing it against the Japanese script, the same kinds of changes kept showing up across the game. Information was removed or replaced, relationships were rewritten, characters were made more romantic or more hostile than they were in Japanese, jokes were swapped for unrelated ones, ordinary terms were turned into newly invented fantasy terminology, and gameplay instructions sometimes stopped saying what the Japanese was actually teaching the player.

A much bigger problem was the overall style of the dialogue. The localization rewrote a huge amount of ordinary Japanese conversation into a deliberately archaic, theatrical kind of English that simply is not there in the original. It wasn't limited to kings, knights or particularly formal scenes either. Villagers, soldiers, bandits and other characters who speak normally in Japanese were often made to sound as though they were all performing in the same pseudo-medieval play.

That also changed characterization in quieter ways. Short replies became speeches. Plain statements acquired metaphors. Characters who were blunt became flowery, characters who were hesitant became confident, and lines that were supposed to sound casual or awkward were polished into something much grander. Taken one at a time, some of those edits can look harmless. Across hundreds of conversations, they give the game a noticeably different voice.

The project therefore stopped being a list of famous localization fixes very early on. We went through the player-facing script against the Japanese and corrected it at the source level instead.

By the end of the review, **more than 25,000 records had been changed or re-translated**. The current canonical script contains **26,551** of them, ranging from one-word fixes to dialogue that had to be translated again from the Japanese. Large parts of the official English did not survive the comparison untouched.

This isn't meant to turn the script into stiff English that copies Japanese grammar. The point is to keep what the original actually says, how the characters actually speak, and the information the game actually gives the player without adding another localization layer on top of it.

## A few examples of what changed

The examples below are deliberately the big, easy-to-show cases. They are not a summary of most of the work.

A much larger problem was the way the localization rewrote the style of ordinary conversations across the game. Plain Japanese dialogue was repeatedly turned into pseudo-archaic or theatrical English even when there was nothing remotely similar in the source. It was not limited to royalty or characters who might plausibly speak in an elevated register either. Villagers, rank-and-file soldiers, mercenaries, bandits and other ordinary characters were written that way too.

Short replies became speeches. Straightforward lines picked up metaphors, jokes or extra attitude. Neutral exchanges gained romance, aggression or drama that was not there in Japanese. Relationship language disappeared, and distinct character voices were often pulled toward the same localized register.

Those changes are harder to demonstrate with one spectacular screenshot, but they account for a huge part of the re-translation. The examples below focus instead on some of the clearest **factual, relationship, story, terminology and gameplay changes**.

**Some contain story spoilers.**

A much larger collection will eventually live in [docs/LOCALIZATION_CHANGES.md](docs/LOCALIZATION_CHANGES.md).

### Ramona is her mother

One conversation contains a very direct statement about Ramona. The English version replaces the relationship with a memory of being found years earlier.

> **Japanese:**  
> あたしも  
> ラモーナ様がお母さんでよかったよ
>
> **Official English:**  
> I'm glad you're the one who found us, all those years ago.
>
> **Revised translation:**  
> I'm glad you're my mother, Ramona-sama.

The Japanese isn't leaving their relationship for the player to infer. She says that Ramona-sama is her mother.

### Nina was never offered a crown here

In one of Alain's conversations with Nina, the official English turns his request into something considerably bigger.

> **Japanese:**  
> だからニーナ、これから  
> ずっと傍で俺と共に戦ってくれないか
>
> **Official English:**  
> Hence I would ask you to stay there as my resplendent queen, for the rest of our mortal days.
>
> **Revised translation:**  
> So, Nina... Will you keep fighting beside me from now on?

Alain asks Nina to keep fighting at his side. There is no *resplendent queen* in the Japanese line, and no promise about the rest of their mortal lives.

This wasn't an isolated case. Several conversations around the Ring of the Maiden were made substantially more romantic in English.

### Primm did not accept a marriage proposal

Another Ring conversation goes even further:

> **Japanese:**  
> 私でよければ、どうか…  
> お傍にいさせてください
>
> **Official English:**  
> I would love nothing more than to be your bride, Alain. I accept, a hundred times over.
>
> **Revised translation:**  
> If you'll have me, please... let me stay by your side.

Primm asks to remain at Alain's side. The bride, the acceptance and the *hundred times over* are all additions.

There are similar changes elsewhere. Monica's admission that Alain is the person she trusts most becomes an explicit *“The one I love.”* Ochlys struggling to admit that she trusts him becomes *“I care for you deeply, Alain.”* Several other scenes add marriage language to promises that never mention marriage in Japanese.

Those additions matter in a game that lets the player choose these relationships.

### The Galerius fight lost part of its own logic

One of the stranger problems appears near the end of the story. The Japanese explicitly explains why the characters need to avoid killing Galerius.

> **Japanese:**  
> …ガレリウス、ようするに  
> アンタを殺さなきゃいいんだろ？
>
> **Official English:**  
> We'll see about that.
>
> **Revised translation:**  
> ...Galerius, that means we just have to stop short of killing you, right?

That is not incidental battle chatter. Killing him is tied to the body-possession problem the characters are trying to solve.

A little later the same point is stated again:

> **Japanese:**  
> 殺さずに…  
> 大人しくさせてやる
>
> **Official English:**  
> Hmph. We'll see about that.
>
> **Revised translation:**  
> I'll make him submit... without killing him.

The official script replaces the plan with generic defiance twice. The battle still happens, but part of the reason the characters fight the way they do has disappeared from the dialogue.

### A tutorial reversed its own counter

The problems aren't confined to story scenes.

One tactical tutorial explains how **Featherswords** can be dealt with:

> **Japanese:**  
> 「シールドシューター」以外にも  
> 対策は練られるはずだ  
> 「ブレイカー」の攻撃を必中に  
> できれば一網打尽にできそうだな
>
> **Official English:**  
> Despite our vulnerability, there exist ways to counteract the Shieldshooter's potency.  
> Granting a Breaker a truestrike attack, for example, could ensure they're defeated in a single strike.
>
> **Revised translation:**  
> Shieldshooters aren't the only way to counter us.  
> Give a Breaker a truestrike attack, and it could take us all out at once.

The subject of the countermeasure was reversed. The Japanese speaker is explaining another way to beat **their own unit type**.

Another tutorial changes Yunifi's useful targets from **scouts** to **cavalry**, then drops the instruction to configure her tactics. A desert NPC who specifically tells the player to prepare a way to **scout for enemies** is reduced to a generic warning to be ready.

Those are gameplay instructions, so we treated them the same way as dialogue rather than assuming the English was close enough.

### The Divine Tree acquired a name that wasn't there

Elheim has a different kind of problem. Several ordinary Japanese terms were turned into special fantasy names in English.

One example is `神樹`, the **Divine Tree**:

> **Japanese:**  
> 神樹のタリスマン
>
> **Official English:**  
> Ervélda's Talisman
>
> **Revised translation:**  
> Divine Tree Talisman

*Ervélda* isn't a reading of `神樹`. It is a new proper name.

The same thing happened with `巫女`, or **priestess**. The localization uses *Turenós*:

> **Japanese:**  
> 巫女の名にかけて…
>
> **Official English:**  
> On my honor as *Turenós*.
>
> **Revised translation:**  
> As a priestess, I swear...

There are several examples of this around Elheim. Respectful forms of address also became fantasy titles, including *Ionísa*, *Ter* and *Terras* in places where the Japanese is using ordinary relationship or honorific language.

We restored what the source actually calls these things instead of trying to preserve a second layer of terminology that only exists in the localization.

### Sometimes a drinking sound really is just a drinking sound

Not every rewrite is complicated.

> **Japanese:**  
> ゴク…ゴク…ゴク…ゴクン
>
> **Official English:**  
> Pray for me, friends. I do this in the name of knowledge.
>
> **Revised translation:**  
> Gulp... Gulp... Gulp... Gulp!

There isn't much to explain here.

## Names, titles and terminology

The official localization changed a surprising amount of terminology beyond ordinary dialogue.

Some changes are simple corrections. Others undo names that were created for descriptive Japanese terms or restore distinctions that had been flattened.

A few examples:

- Ervélda → **Divine Tree** (神樹)
- Forest of the Fae → **Forest of Spirits** (精霊の森)
- Nasthírion → **Tower of Thorns** (茨の塔)
- Turenós → **Priestess** (巫女)
- Province of the Ashen Blue → **Knights' Domain** (騎士団領)
- Salamander Knights → **Crimson Dragon Knights** (紅龍騎士団)
- The Holy March → **Road to the Grand Cathedral** (大聖堂への道)
- Witches' Swamp → **Witches' Village** (魔女の里)
- Ancient Dragon Skull → **Ancient Dragon Bone** (古代竜の骨)
- Quill → **Pencil** (鉛筆)

Skills received the same treatment. Among the restored names are:

- Nature's Wrath → **Gremlin Grave** (グレムリングレイブ)
- United Front → **Limit Force** (リミットフォース)
- Boon of the Faeries → **Spirits' Blessing** (精霊の加護)
- Rage of the Faeries → **Spirits' Wrath** (精霊の怒り)
- Vicious Torment → **Diabonator** (ディアボネイター)
- Firestorm → **Firenate** (ファイアネイト)
- Icestorm → **Icenate** (アイスネイト)
- Heavy Slash → **Mega Slash** (メガスラッシュ)
- Powerful Impact → **Muscle Impact** (マッスルインパクト)

Quest and chapter titles were checked too. Some of the English titles bear surprisingly little resemblance to the Japanese:

- *Province of Famine* → **Retaking the Knights' Domain**
- *Dying Breath of an Empire Fallen* → **The Capital of Calamity Reborn**
- *Bound by Sacred Oath* → **The Robed Knight**
- *The Priestess, Abducted* → **The Priestess's Whereabouts**
- *The Ravaged Swamp* → **Witch of the Swamp**
- *To the Sanctuaries* → **Six Sages of the Sanctuaries**

There are also many NPC and generated-name corrections. We haven't put the whole list here because it becomes unreadable very quickly. The longer translation document will contain those.

## Honorifics and the two versions

The project has two versions:

**Honorifics** is the main and canonical translation. It keeps confirmed Japanese forms of address where they are actually used in the original dialogue: suffixes such as *-san*, *-sama*, *-chan* and *-kun*, along with relationship forms such as **Onee-sama** where those forms are part of the way the characters speak to one another.

This matters particularly in Unicorn Overlord because the localization didn't merely remove honorifics. In some places it replaced ordinary Japanese forms of address with invented fantasy terminology. `お姉様`, for example, became *Ionísa* in parts of Elheim. The Honorifics version restores **Onee-sama**.

**No Honorifics** uses the same re-translation, restored names and restored terminology, but removes Japanese honorifics where an alternate English form is needed.

And to answer a question that would otherwise come up sooner or later: **no, there will not be a third version that keeps the localized character names, place names, skill names or invented terminology.**

Those changes are part of what this project is correcting. Maintaining a separate script that puts them back would mean maintaining a second localization on top of the re-translation, and that is not what this patch is for.

The No Honorifics version is therefore an honorific preference, not a "keep the official localization names" version.

## Character relationships and tone

A lot of the work couldn't be reduced to fixing nouns.

Unicorn Overlord has many short support-style conversations where a small change can alter how two people come across. The official English regularly makes those exchanges more romantic, more dramatic, more confrontational or more elaborate than the source.

We restored family relationships that had been blurred or replaced, including Ramona being explicitly called someone's mother, Josef speaking specifically as Alain's adoptive father, and Gilbert asking whether Ludwig can still think of him as his younger brother.

We also removed romantic statements where the Japanese only expresses trust, loyalty or a wish to stay together. This was particularly noticeable around Ring of the Maiden conversations. The revised script doesn't try to tone those scenes down. It restores the level of intimacy that is actually there instead of adding another one.

Character quirks matter for the same reason. Belisarios, for example, refers to himself grandly as **Belisarios-sama** in Japanese. The official line turns that into a generic remark about tasting blood. The revised version keeps his ridiculous third-person self-importance because that is how he actually talks.

There are hundreds of smaller examples that work the same way: a character becomes less formal, more formal, rougher, gentler, more poetic or more confident because the English decided to rewrite the line rather than translate it.

## What the translation covers

We didn't stop after the main story.

The review covers player-facing text throughout the game, including:

- story dialogue and narration;
- rapport and character conversations;
- battle dialogue;
- quest and chapter titles;
- choices and system messages;
- tutorials and tactical advice;
- class and skill names;
- item and equipment names and descriptions;
- locations, factions and organizations;
- archive, help and reference text;
- other player-facing text found in the game's script data.

The whole script went through the main JP-to-English review and then additional passes focused on fidelity, consistency and natural English. The Honorifics version is the canonical script, so that is also the version that received the bulk of the direct review and testing. The No Honorifics edition is built from it as a sparse set of alternate forms rather than as a separately translated script.

## Japanese voices

The project is intended to use the original Japanese performances.

The patch removes the English-voice choice from the intended setup and keeps the Japanese voices as the version the translation is written around.

## Current status

This project is still a **beta**.

The translation work has gone through several full review passes, and a large amount of static and build-side testing has already been done, but this is not being presented as a final finished release yet.

The Honorifics version is the canonical script and has been the main target of testing. The No Honorifics edition follows the same translation and differs only where its alternate address forms require it.

Public testing is still important. There may be:

- minor translation mistakes that survived the review;
- awkward wording that only becomes obvious in the actual scene;
- typos;
- text that needs another look once surrounding dialogue is seen in-game;
- font or glyph problems;
- wrapping or textbox-fit problems;
- control-code or link issues;
- problems on less common menu or UI screens;
- save/load or patch-installation issues;
- other technical problems that only show up during normal play.

The translation itself can still change during the beta if testing finds something wrong. The same applies to the rest of the patch.

Reports are welcome, especially when they include a screenshot and enough context to identify the scene or menu involved.

## Feedback and bug reports

We do want reports during the beta, especially when something can actually be checked.

Useful reports include things like:

- a line that appears to mistranslate the Japanese;
- missing information or something added that isn't in the original;
- a character speaking in the wrong register or using the wrong form of address;
- inconsistent names or terminology;
- typos;
- text overflow, bad line breaks or broken formatting;
- a menu, link, control code or other part of the patch behaving incorrectly;
- anything else that can be tied to a specific scene, record or in-game problem.

A screenshot and a little surrounding context help a lot. If the report is about the translation itself, even better if it points to the relevant Japanese wording.

What isn't useful is feedback that simply asks us to put the localization back.

We are not going to restore localized names because somebody prefers them, remove Japanese relationship language from the Honorifics version, bring back invented terminology, rewrite ordinary dialogue into faux-archaic English again, or preserve a changed line just because it sounds more dramatic in English.

Likewise, *"the official line sounds better to me"* isn't enough by itself when the official line says something different from the Japanese. If there is a real problem with our wording, context, interpretation or English, we absolutely want to know. If the objection is simply that the re-translation is less like the localization, that is the point of the patch.

We are happy to correct mistakes. We are not interested in turning the project back into the thing it was made to replace.

## Building from source

The repository contains the translation data and the build tools. It does **not** contain the game.

You need your own supported Western copy of Unicorn Overlord and the required game data.

The easiest way to start the toolkit is:

```bash
./run-uo-unburger.sh
```

The launcher builds the local tool when needed and opens the normal interface.

For command-line use, the translation tables are:

```text
translations/honorifics.tsv
translations/no-honorifics.tsv
```

The Honorifics table is the canonical translation. The No Honorifics file contains only the overrides needed for that edition.

A typical non-interactive build can also be run from the CLI once the required source data is available. The tool supports building both variants from the same translation tree.

The generated build and release state lives under `.local/`. That directory is intentionally machine-local and isn't part of the public repository.

If you are starting from a Switch image rather than an already extracted supported CPK, the toolkit can use supported external extraction tools and user-provided keys. None of those game files or keys are included here.

## Documentation

- [Notable localization changes](docs/LOCALIZATION_CHANGES.md)
- [Building from source](docs/BUILDING.md)

The localization-changes document is the longer, spoiler-heavy version of the examples above. It covers names, terminology, relationship changes, story facts, gameplay instructions and other cases that would make this README absurdly long if we tried to put them all here.

This is an unofficial fan translation and restoration patch for **Unicorn Overlord**.

You need your own copy of the game. The repository and eventual patch packages do not distribute the original game data.

## Credits

**Translation:** kyuubo  
**Check, toolchain and TL consultation:** rsc-pl  
**Additional TL check:** Mblack
