# wordfreq attribution

The count column of `lexicon.tsv` comes from **wordfreq 3.1.1** by Robyn Speer
(https://github.com/rspeer/wordfreq; citation: Speer 2022, *rspeer/wordfreq: v3.0*,
https://doi.org/10.5281/zenodo.7199437). Package code: Apache License 2.0
(`wordfreq-LICENSE.txt`). Its data, and the count column derived from it:
Creative Commons Attribution-ShareAlike 4.0, https://creativecommons.org/licenses/by-sa/4.0/.

wordfreq's English frequencies combine Wikipedia, subtitles (OpenSubtitles, SUBTLEX),
news, books (Google Books Ngrams), web text (ParaCrawl, Leeds Internet Corpus),
Twitter and Reddit. Only the stored frequency table is used. Each frequency is
multiplied by a fixed scale (5.88e11) and rounded to a whole count. That scale
only sets the units; the program uses ratios of counts.

SUBTLEX credit: Brysbaert & New (2009), *Behavior Research Methods* 41, 977–990;
van Heuven, Mandera, Keuleers & Brysbaert (2014), *Quarterly Journal of
Experimental Psychology* 67, 1176–1190.
